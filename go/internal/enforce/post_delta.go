package enforce

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"purser-hook/internal/ledger"
	"purser-hook/internal/review"
)

// assemblePostDelta mirrors the delta selection and attribution block in
// codex.post_tool_use. Git inspection errors are returned to the post boundary.
func assemblePostDelta(repoRoot string, pending, bundle map[string]any, protectedPaths []string, foreignOwners []map[string]any) (map[string]any, error) {
	phase, _ := pending["phase"].(string)
	scope, scopeOK := postStringList(pending["scope"])
	baseline, baselineOK := pending["baseline"].(map[string]any)
	worktree, _ := pending["worktree_path"].(string)
	deltaRoot := repoRoot
	if worktree != "" {
		deltaRoot = worktree
		bundle["apply"] = "codex_apply"
		if snapshot, ok := pending["worktree_snapshot"].(string); ok && snapshot != "" {
			if digest, err := ledger.JobChangedPathsSHA256(worktree, snapshot); err == nil {
				bundle["changed_paths_sha256"] = digest
			}
		}
	}
	var delta map[string]any
	if !scopeOK || !baselineOK {
		return nil, fmt.Errorf("post delta input is malformed")
	}
	if deltaRoot == "" || baseline["status"] != ledger.OK {
		code, _ := baseline["code"].(string)
		if code == "" {
			code = "BASELINE_UNAVAILABLE"
		}
		message, _ := baseline["message"].(string)
		if message == "" {
			message = "The baseline is unavailable."
		}
		delta = map[string]any{"status": "UNKNOWN", "code": code, "message": message,
			"baseline_head": baseline["baseline_head"], "current_head": nil, "changed_paths": nil}
	} else if worktree != "" {
		snapshot, _ := pending["worktree_snapshot"].(string)
		delta = ledger.ComputeJobDelta(deltaRoot, snapshot)
		var err error
		delta, err = ledger.FilterJobDeltaChecked(deltaRoot, delta)
		if err != nil {
			return nil, err
		}
	} else {
		var requested []string
		if phase != "plan" && len(scope) > 0 {
			requested = scope
		}
		delta = ledger.ComputeDelta(deltaRoot, baseline, requested)
		baseHead, _ := delta["baseline_head"].(string)
		currentHead, _ := delta["current_head"].(string)
		changed, _ := postStringList(delta["changed_paths"])
		if delta["status"] == ledger.OK && phase != "plan" && (currentHead != baseHead || (phase == "operation" && len(changed) > 0)) {
			delta = map[string]any{"status": "UNKNOWN", "code": "EXTERNAL_REPOSITORY_CHANGE",
				"message":       "Repository source changed during the job; the external change cannot be attributed to the worker.",
				"baseline_head": delta["baseline_head"], "current_head": delta["current_head"], "changed_paths": nil}
		}
	}
	bundle["baseline_head"], bundle["current_head"] = delta["baseline_head"], delta["current_head"]
	forceFull := delta["status"] != ledger.OK || bundle["force_full_review"] == true
	var anomaly map[string]any
	if delta["status"] == ledger.OK && phase == "implementation" && len(scope) > 0 && baseline["status"] == ledger.OK {
		if worktree != "" {
			changed, _ := postStringList(delta["changed_paths"])
			outside := []string{}
			for _, path := range changed {
				if !postPathInScope(path, scope) {
					outside = append(outside, path)
				}
			}
			anomaly = map[string]any{"status": ledger.OK, "code": "OUT_OF_SCOPE_CHECK_COMPLETED", "paths": outside}
		} else {
			anomaly = ledger.DetectOutOfScopeModifications(deltaRoot, baseline, scope)
		}
	}
	bundle["whole_tree_anomaly"] = anomaly
	if delta["status"] == ledger.OK && (phase == "plan" || (phase == "implementation" && len(scope) > 0)) {
		if phase == "implementation" {
			bundle["whole_tree_delta"] = map[string]any{"changed_paths": delta["changed_paths"],
				"changed_file_count": delta["changed_file_count"], "changed_line_count": delta["changed_line_count"]}
		}
		owner, ownerOK := pending["lease_owner"].(string)
		if ownerOK && owner != "" {
			attributed, known := ledger.AttributeKnownConcurrentScopes(delta, scopeIfImplementation(phase, scope), foreignOwners, &protectedPaths, anomalyPaths(anomaly))
			delta = attributed
			bundle["known_concurrent_scopes"] = known
		} else {
			forceFull = true
			bundle["findings"] = appendPostFinding(bundle["findings"], "LEASE_HISTORY_UNAVAILABLE", "This call has no scope lease ownership record.")
		}
	}
	if delta["status"] == ledger.OK && phase == "plan" {
		changed, _ := postStringList(delta["changed_paths"])
		if len(changed) > 0 || delta["current_head"] != delta["baseline_head"] {
			if len(changed) > 0 {
				bundle["findings"] = appendPostFinding(bundle["findings"], "PLAN_MUTATED_REPO",
					"Repository source changed during a read-only plan call; a concurrent writer may be the cause: "+postJoinFirst(changed, 10))
			}
			delta = map[string]any{"status": "UNKNOWN", "code": "EXTERNAL_REPOSITORY_CHANGE",
				"message":       "Repository changes could not be attributed to this plan job.",
				"baseline_head": delta["baseline_head"], "current_head": delta["current_head"], "changed_paths": nil}
			forceFull = true
		}
	}
	if delta["status"] == ledger.OK {
		changed, _ := postStringList(delta["changed_paths"])
		if len(changed) > 0 {
			if _, diff, diffErr := review.DelegatedDiff(deltaRoot, delta); diffErr == nil {
				for _, candidate := range review.TestWeakeningCandidates(diff) {
					code := "TEST_ASSERTION_REMOVED_CANDIDATE"
					if strings.Contains(candidate, "TEST_SKIP_ADDED") {
						code = "TEST_SKIP_ADDED_CANDIDATE"
					}
					bundle["findings"] = appendPostFinding(bundle["findings"], code, candidate+"; inspect manually, not a confirmed weakening.")
				}
			}
		}
		lines, _ := delta["changed_line_count"].(int)
		files, _ := delta["changed_file_count"].(int)
		bundle["changed_paths"], bundle["changed_path_count"], bundle["delta_lines"], bundle["delta_files"] = changed, len(changed), lines, files
		if len(scope) > 0 {
			classification := ledger.ClassifyConcurrentWriters(delta, scope, nil, nil, &protectedPaths)
			bundle["protected_paths_touched"] = classification["protected_paths"]
			bundle["concurrent_writer"] = classification["concurrent_writer"]
		} else {
			bundle["protected_paths_touched"] = ledger.DetectProtectedPaths(delta, &protectedPaths)
		}
	} else {
		code, _ := delta["code"].(string)
		if code == "" {
			code = "UNKNOWN_DELTA"
		}
		message, _ := delta["message"].(string)
		if message == "" {
			message = "Delta is UNKNOWN."
		}
		bundle["findings"] = appendPostFinding(bundle["findings"], code, message)
	}
	if anomaly != nil {
		if anomaly["status"] == ledger.OK {
			known := postKnownPaths(bundle["known_concurrent_scopes"])
			paths, _ := postStringList(anomaly["paths"])
			filtered := make([]string, 0, len(paths))
			for _, path := range paths {
				if !known[path] {
					filtered = append(filtered, path)
				}
			}
			filteredAnomaly := make(map[string]any, len(anomaly))
			for key, value := range anomaly {
				filteredAnomaly[key] = value
			}
			filteredAnomaly["paths"] = filtered
			anomaly = filteredAnomaly
		}
		forceFull = applyOutOfScopeResult(bundle, anomaly, protectedPaths) || forceFull
	}
	if _, recorded := pending["main_checkout_baseline"]; recorded && worktree != "" && phase == "implementation" {
		forceFull = inspectMainCheckout(repoRoot, mapValue(pending["main_checkout_baseline"]), scope, delta, bundle) || forceFull
	}
	if delta["status"] != ledger.OK {
		forceFull = true
	}
	bundle["force_full_review"] = forceFull
	bundle["delta"] = delta
	return bundle, nil
}

const mainCheckoutPathCap = 50

// CaptureMainCheckoutBaseline records bounded status and file metadata for worktree jobs.
func CaptureMainCheckoutBaseline(root string, entries any) map[string]any {
	if entries == nil {
		var err error
		entries, err = ledger.StatusEntries(root)
		if err != nil {
			return map[string]any{"status": "UNKNOWN"}
		}
	}
	statuses, ok := postStatusEntries(entries)
	if !ok {
		return map[string]any{"status": "UNKNOWN"}
	}
	paths := make([]string, 0, len(statuses))
	for path := range statuses {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	truncated := len(paths) > mainCheckoutPathCap
	paths = postFirst(paths, mainCheckoutPathCap)
	recorded := make([]map[string]string, 0, len(paths))
	fingerprints := make(map[string]any, len(paths))
	status := ledger.OK
	for _, path := range paths {
		recorded = append(recorded, map[string]string{"path": path, "state": statuses[path]})
		fingerprint, err := mainCheckoutFingerprint(root, path)
		if err != nil {
			status = "UNKNOWN"
			continue
		}
		fingerprints[path] = fingerprint
	}
	return map[string]any{"status": status, "status_entries": recorded, "fingerprints": fingerprints, "truncated": truncated}
}

func mainCheckoutFingerprint(root, path string) (string, error) {
	if !filepath.IsLocal(filepath.FromSlash(path)) {
		return "", os.ErrInvalid
	}
	file := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(file)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err == nil && info.Size() >= 0 && !info.ModTime().IsZero() {
		return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano()), nil
	}
	// Only read content when size or modification time could not be obtained.
	reader, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

func inspectMainCheckout(root string, baseline map[string]any, scope []string, delta, bundle map[string]any) bool {
	baselinePaths, baselineOK := postStatusEntries(baseline["status_entries"])
	fingerprints, fingerprintsOK := baseline["fingerprints"].(map[string]any)
	entries, err := ledger.StatusEntries(root)
	currentPaths, currentOK := postStatusEntries(entries)
	truncated, truncatedOK := baseline["truncated"].(bool)
	unknown := baseline["status"] != ledger.OK || !baselineOK || !fingerprintsOK || !truncatedOK || err != nil || !currentOK || truncated
	changed := map[string]bool{}
	if baselineOK && err == nil && currentOK {
		for path, state := range currentPaths {
			previous, recorded := baselinePaths[path]
			if recorded && previous != state || !recorded && !truncated && truncatedOK {
				changed[path] = true
			}
		}
	}
	for path := range baselinePaths {
		previous, ok := fingerprints[path].(string)
		if !ok || previous == "" {
			unknown = true
			continue
		}
		current, err := mainCheckoutFingerprint(root, path)
		if err != nil {
			unknown = true
		} else if current != previous {
			// This also catches deletion and reversion to clean. A commit that
			// only clears status leaves this fingerprint unchanged.
			changed[path] = true
		}
	}
	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > 0 {
		preview := postFirst(paths, mainCheckoutPathCap)
		message := "Main checkout changed during this worktree job: " + strings.Join(preview, ", ") + ". The worker may have written outside its worktree, or someone else edited these files."
		files, filesOK := pythonInt(delta["changed_file_count"])
		lines, linesOK := pythonInt(delta["changed_line_count"])
		allInScope := len(paths) > 0
		for _, path := range paths {
			allInScope = allInScope && postPathInScope(path, scope)
		}
		if filesOK && linesOK && files == 0 && lines == 0 && allInScope {
			message += " These changed files are within the job scope, but the worktree has no delta; Claude may adopt them after review."
		}
		bundle["findings"] = appendPostFinding(bundle["findings"], "MAIN_CHECKOUT_CHANGED_DURING_WORKTREE_JOB", message)
		findings := bundle["findings"].([]any)
		finding := findings[len(findings)-1].(map[string]any)
		finding["paths"], finding["path_count"], finding["truncated"] = preview, len(paths), len(paths) > mainCheckoutPathCap
	}
	if unknown {
		bundle["findings"] = appendPostFinding(bundle["findings"], "MAIN_CHECKOUT_STATUS_UNKNOWN", "Main checkout inspection is UNKNOWN; some paths could not be compared.")
		findings := bundle["findings"].([]any)
		findings[len(findings)-1].(map[string]any)["truncated"] = truncated
	}
	return unknown || len(paths) > 0
}

func postStatusEntries(value any) (map[string]string, bool) {
	result := map[string]string{}
	add := func(path, state string) {
		result[path] = state
	}
	switch entries := value.(type) {
	case []map[string]string:
		for _, entry := range entries {
			path, pathOK := entry["path"]
			state, stateOK := entry["state"]
			if !pathOK || !stateOK {
				return nil, false
			}
			add(path, state)
		}
	case []any:
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				return nil, false
			}
			path, pathOK := entry["path"].(string)
			state, stateOK := entry["state"].(string)
			if !pathOK || !stateOK {
				return nil, false
			}
			add(path, state)
		}
	default:
		return nil, false
	}
	return result, true
}

func postFirst(paths []string, limit int) []string {
	if len(paths) > limit {
		paths = paths[:limit]
	}
	return paths
}

func scopeIfImplementation(phase string, scope []string) []string {
	if phase == "implementation" {
		return scope
	}
	return nil
}

func anomalyPaths(anomaly map[string]any) []string {
	if anomaly == nil || anomaly["status"] != ledger.OK {
		return nil
	}
	paths, _ := postStringList(anomaly["paths"])
	return paths
}

func postPathInScope(path string, scope []string) bool {
	for _, item := range scope {
		if item == "." || path == item || len(path) > len(item) && path[:len(item)+1] == item+"/" {
			return true
		}
	}
	return false
}

func appendPostFinding(value any, code, message string, reference ...string) []any {
	findings, ok := value.([]any)
	if !ok {
		findings = []any{}
	}
	finding := map[string]any{"code": code, "message": message}
	if len(reference) > 0 && reference[0] != "" {
		finding["reference"] = reference[0]
	}
	return append(findings, finding)
}

func postKnownPaths(value any) map[string]bool {
	result := map[string]bool{}
	items, ok := value.([]map[string]string)
	if ok {
		for _, item := range items {
			if path, valid := item["path"]; valid {
				result[path] = true
			}
		}
		return result
	}
	if items, valid := value.([]any); valid {
		for _, raw := range items {
			item, valid := raw.(map[string]any)
			if !valid {
				continue
			}
			if path, valid := item["path"].(string); valid {
				result[path] = true
			}
		}
	}
	return result
}

func postJoinFirst(paths []string, limit int) string {
	if len(paths) > limit {
		paths = paths[:limit]
	}
	return strings.Join(paths, ", ")
}
