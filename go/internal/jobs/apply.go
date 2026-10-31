package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/enforce"
	"purser-hook/internal/lease"
	"purser-hook/internal/ledger"
	"purser-hook/internal/proc"
)

// Apply applies a succeeded worktree job to its repository. stateRoot is the
// Purser state directory that contains codex_exec_mcp/jobs.
func Apply(stateRoot string, args map[string]any, options ...ApplyOptions) map[string]any {
	var opt ApplyOptions
	if len(options) != 0 {
		opt = options[0]
	}
	result := applyToolResult
	resolved := false
	if value, ok := args["resolved"]; ok {
		var valid bool
		resolved, valid = value.(bool)
		if !valid {
			return result(map[string]any{"applied": false, "reason": "resolved must be a boolean."})
		}
	}
	acceptOnly := false
	if value, ok := args["acceptOnly"]; ok {
		var valid bool
		acceptOnly, valid = value.(bool)
		if !valid {
			return result(map[string]any{"applied": false, "reason": "acceptOnly must be a boolean; use true to record acceptance without applying."})
		}
	}
	id, _ := args["jobId"].(string)
	dir := filepath.Join(stateRoot, "codex_exec_mcp", "jobs")
	job := readJob(dir, id)
	if job == nil {
		return result(map[string]any{"state": "FAILED", "errorCode": "JOB_NOT_FOUND", "message": "Job record is unavailable"})
	}
	if !maintenanceTestJob(job) {
		RecoverApplyValidations(stateRoot)
	}
	var jobID, state, worktree, repo, snapshot string
	job.Get("jobId", &jobID)
	job.Get("state", &state)
	job.Get("worktreePath", &worktree)
	job.Get("repoRoot", &repo)
	job.Get("snapshot", &snapshot)
	var conflictReason string
	job.Get("applyConflictReason", &conflictReason)
	var discarded bool
	job.Get("discarded", &discarded)
	if discarded {
		return result(map[string]any{"jobId": jobID, "errorCode": "DISCARDED", "message": "This job was discarded and cannot be applied."})
	}
	if worktree == "" {
		return result(map[string]any{"state": "FAILED", "errorCode": "NOT_WORKTREE_JOB", "message": "Job has no worktree"})
	}
	if state != "SUCCEEDED" && !(resolved && state == "FAILED" && conflictReason == "worker_replay") {
		return result(map[string]any{"state": "FAILED", "errorCode": "JOB_NOT_READY", "message": "Job has not succeeded. Follow the start response's wait instructions, then call codex_result."})
	}
	root, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return result(map[string]any{"state": "FAILED", "errorCode": "REPOSITORY_UNAVAILABLE", "message": err.Error()})
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return result(map[string]any{"state": "FAILED", "errorCode": "REPOSITORY_UNAVAILABLE", "message": err.Error()})
	}
	digest := sha256.Sum256([]byte(root))
	unlock, err := lease.Lock(filepath.Join(filepath.Dir(dir), "apply", hex.EncodeToString(digest[:])+".lock"))
	if err != nil {
		return result(map[string]any{"state": "FAILED", "errorCode": "APPLY_LOCK_UNAVAILABLE", "message": err.Error()})
	}
	defer func() { _ = unlock() }()
	job = readJob(dir, jobID)
	if job == nil {
		return result(map[string]any{"state": "FAILED", "errorCode": "JOB_NOT_FOUND", "message": "Job record is unavailable"})
	}
	var applyState string
	job.Get("applyState", &applyState)
	job.Get("applyConflictReason", &conflictReason)
	job.Get("worktreePath", &worktree)
	job.Get("repoRoot", &repo)
	job.Get("snapshot", &snapshot)
	if maintenanceTestJob(job) && applyState != "applied" {
		if code, message := verifyMaintenanceApply(stateRoot, root, job, worktree, snapshot, args); code != "" {
			return result(map[string]any{"state": "FAILED", "errorCode": code, "message": message})
		}
	}
	if maintenanceTestJob(job) && applyState == "applied" {
		RecoverApplyValidations(stateRoot)
	}
	if acceptOnly {
		sessionID, _ := args["sessionId"].(string)
		revision, _ := args["revision"].(string)
		var reviewedSnapshot string
		job.Get("reviewedSnapshot", &reviewedSnapshot)
		current, digestErr := changedPathsDigest(worktree, snapshot)
		if sessionID == "" || reviewedSnapshot == "" || digestErr != nil || revision == "" || revision != current {
			return result(map[string]any{"accepted": false, "reason": "Reviewed revision is unavailable or stale. Call codex_result, review the refreshed bundle, then retry acceptance."})
		}
		acceptance := map[string]any{"revision": current, "sessionId": sessionID, "acceptedAt": time.Now().UTC().Format(time.RFC3339Nano)}
		if err := job.Set("acceptance", acceptance); err != nil {
			return result(map[string]any{"accepted": false, "reason": "acceptance could not be recorded"})
		}
		if err := Write(filepath.Join(dir, jobID+".json"), job); err != nil {
			return result(map[string]any{"accepted": false, "reason": "acceptance could not be recorded"})
		}
		return result(map[string]any{"accepted": true, "revision": current})
	}
	var saved map[string]any
	if applyState == "applied" && job.Get("applyResult", &saved) && saved != nil {
		if err := recordPlanApply(stateRoot, recordMap(job), jobID); err != nil {
			saved["plan_warning"] = err.Error()
		}
		enqueueAppliedValidation(stateRoot, recordMap(job), saved)
		addReadyPlanMilestones(stateRoot, recordMap(job), saved)
		recordApplyDecision(opt, stateRoot, recordMap(job), "accepted", "apply", jobID, args["reviewer1Valid"], args["reviewer1False"])
		releaseAppliedJobLeases(opt, stateRoot, dir, worktree)
		dispatchWorktreeRetirement(stateRoot, jobID)
		return result(saved)
	}
	if !resolved {
		var acceptance map[string]any
		job.Get("acceptance", &acceptance)
		current, digestErr := changedPathsDigest(worktree, snapshot)
		if digestErr != nil {
			return result(map[string]any{"applied": false, "reason": "main Claude has not accepted this revision"})
		}
		if acceptance == nil || acceptance["revision"] != current {
			if acceptance != nil {
				job.Delete("acceptance")
				_ = Write(filepath.Join(dir, jobID+".json"), job)
			}
			revision, _ := args["revision"].(string)
			var reviewed string
			if revision == "" || revision != current || !job.Get("reviewedSnapshot", &reviewed) || reviewed == "" {
				return result(map[string]any{"applied": false, "reason": "main Claude has not accepted this revision"})
			}
			sessionID, _ := args["sessionId"].(string)
			acceptance = map[string]any{"revision": current, "sessionId": sessionID, "acceptedAt": time.Now().UTC().Format(time.RFC3339Nano)}
			if err := job.Set("acceptance", acceptance); err != nil || (!maintenanceTestJob(job) && Write(filepath.Join(dir, jobID+".json"), job) != nil) {
				return result(map[string]any{"applied": false, "reason": "main Claude has not accepted this revision"})
			}
		}
	}
	if resolved {
		if applyState != "conflict" && conflictReason != "worker_replay" {
			return result(map[string]any{"applied": false, "reason": "Job is not in conflict state."})
		}
		return applyResolvedJob(stateRoot, dir, jobID, job, repo, worktree, snapshot, args, opt)
	}
	if info, e := os.Stat(worktree); e != nil || !info.IsDir() {
		return result(map[string]any{"state": "FAILED", "errorCode": "WORKTREE_MISSING", "message": "Job worktree is unavailable"})
	}
	patch, err := ledger.JobPatch(worktree, snapshot)
	if err != nil {
		return result(map[string]any{"state": "FAILED", "errorCode": "PATCH_UNAVAILABLE", "message": err.Error()})
	}
	paths, err := patchPaths(root, patch)
	if err != nil {
		return result(map[string]any{"state": "FAILED", "errorCode": "PATCH_PATHS_UNAVAILABLE", "message": err.Error()})
	}
	if len(paths) != 0 {
		allPresent := true
		for _, path := range paths {
			if !ledger.SameWorktreeContent(worktree, root, path) {
				allPresent = false
				break
			}
		}
		if allPresent {
			return persistApplied(stateRoot, dir, jobID, job, map[string]any{"applied": true, "paths": paths}, args, opt, "")
		}
	}
	if _, ok := job.values["applyResult"]; ok {
		_ = job.Get("applyResult", &saved)
		return result(saved)
	}
	var reviewed string
	if job.Get("reviewedSnapshot", &reviewed) && reviewed != "" {
		drift, e := ledger.SnapshotDriftPaths(root, reviewed, paths)
		if e != nil {
			return result(map[string]any{"state": "FAILED", "errorCode": "SNAPSHOT_UNAVAILABLE", "message": e.Error()})
		}
		if len(drift) != 0 {
			job.Delete("acceptance")
			var revision int64
			job.Get("snapshotRevision", &revision)
			_ = job.Set("snapshotRevision", revision+1)
			_ = job.Set("reviewRefreshPending", true)
			delete(job.values, "reviewedSnapshot")
			for i, key := range job.keys {
				if key == "reviewedSnapshot" {
					job.keys = append(job.keys[:i], job.keys[i+1:]...)
					break
				}
			}
			if err := Write(filepath.Join(dir, jobID+".json"), job); err != nil {
				return result(map[string]any{"state": "FAILED", "errorCode": "JOB_WRITE_FAILED", "message": err.Error()})
			}
			index := filepath.Join(stateRoot, "async_jobs", jobID+".json")
			var value map[string]any
			b, e := os.ReadFile(index)
			if e == nil {
				e = json.Unmarshal(b, &value)
			}
			if e == nil && value == nil {
				e = errors.New("async job record must be a JSON object")
			}
			if e == nil {
				value["stage"] = "REVIEW_REFRESH"
				b, e = json.Marshal(value)
			}
			if e == nil {
				e = atomicWrite(index, b)
			}
			if e != nil {
				return result(map[string]any{"applied": false, "errorCode": "REVIEW_REFRESH_UNAVAILABLE", "message": "Job was re-prepared but review refresh could not be scheduled."})
			}
			return result(map[string]any{"applied": false, "errorCode": "REVIEW_STALE_REPREPARE_REQUIRED", "message": "Checkout changed in the reviewed job paths. Call codex_result to re-prepare and review the diff before applying.", "paths": drift})
		}
	}
	var outcome map[string]any
	if maintenanceTestJob(job) {
		outcome = applyMaintenancePatch(root, patch)
	} else {
		outcome = ledger.ApplyJobPatch(root, patch)
	}
	applied, _ := outcome["applied"].(bool)
	value := map[string]any{"applied": applied}
	if applied {
		value["paths"] = paths
	} else {
		value["conflicts"] = outcome["conflicts"]
		value["applicable"] = outcome["applicable"]
		value["worktreePath"] = worktree
	}
	if !applied && maintenanceTestJob(job) {
		return result(map[string]any{"state": "FAILED", "errorCode": "MAINTENANCE_PROPOSAL_CONFLICT", "message": "The maintenance proposal conflicts with the clean baseline and was not applied."})
	}
	_ = job.Set("applyState", map[bool]string{true: "applied", false: "conflict"}[applied])
	_ = job.Set("applyResult", value)
	if applied {
		if maintenanceTestJob(job) {
			if revision, e := changedPathsDigest(worktree, snapshot); e == nil {
				_ = job.Set("appliedRevision", revision)
			}
			if acceptance, ok := recordMap(job)["acceptance"].(map[string]any); !ok || acceptance["revision"] != args["revision"] {
				_ = job.Set("acceptance", map[string]any{"revision": args["revision"], "sessionId": args["sessionId"], "acceptedAt": time.Now().UTC().Format(time.RFC3339Nano)})
			}
		}
		_ = job.Set("appliedAt", float64(time.Now().UnixNano())/1e9)
		if task, _ := recordMap(job)["taskId"].(string); task != "" {
			_ = job.Set("decisionRecorded", true)
		}
	}
	if err := Write(filepath.Join(dir, jobID+".json"), job); err != nil {
		return result(map[string]any{"state": "FAILED", "errorCode": "JOB_WRITE_FAILED", "message": err.Error()})
	}
	if applied {
		values := recordMap(job)
		supersedeEarlierTaskJobs(stateRoot, jobID, values)
		if err := recordPlanApply(stateRoot, values, jobID); err != nil {
			value["plan_warning"] = err.Error()
		}
		enqueueAppliedValidation(stateRoot, values, value)
		addReadyPlanMilestones(stateRoot, values, value)
		recordScopeOutcome(opt, stateRoot, recordMap(job), "applied")
		recordApplyDecision(opt, stateRoot, recordMap(job), "accepted", "apply", jobID, args["reviewer1Valid"], args["reviewer1False"])
		recordApplyObservation(stateRoot, recordMap(job), value)
		releaseAppliedJobLeases(opt, stateRoot, dir, worktree)
		dispatchWorktreeRetirement(stateRoot, jobID)
	}
	return result(value)
}

func maintenanceTestJob(job *Record) bool {
	var maintenance bool
	return job != nil && job.Get("maintenance_test_job", &maintenance) && maintenance
}

func verifyMaintenanceApply(stateRoot, root string, job *Record, worktree, snapshot string, args map[string]any) (string, string) {
	proposalPath := filepath.Join(stateRoot, "projects", contextio.ProjectKey(root), "maintenance", "reports", "test-gaps", "ready.json")
	data, err := os.ReadFile(proposalPath)
	if err != nil {
		return "MAINTENANCE_PROPOSAL_STALE", "The ready maintenance proposal is missing or unavailable."
	}
	var proposal map[string]any
	if json.Unmarshal(data, &proposal) != nil || proposal["status"] != "ready" {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The ready maintenance proposal is invalid."
	}
	var baseline, source, recordedID string
	job.Get("maintenance_identity_source", &source)
	job.Get("maintenance_job_id", &recordedID)
	if source != "host" || recordedID != stringValue(recordMap(job)["jobId"]) {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The maintenance job identity is not host recorded."
	}
	baseline, _ = proposal["baseline_head"].(string)
	if baseline == "" || proposal["job_id"] != recordMap(job)["jobId"] || !sameApplyProjectPath(root, stringValue(proposal["project_root"])) {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The ready proposal does not belong to this maintenance job and project."
	}
	var hostRun MaintenanceTestRun
	if !job.Get("maintenanceTests", &hostRun) || hostRun.JobID != stringValue(recordMap(job)["jobId"]) || hostRun.BaselineHead != baseline || hostRun.BaselineSnapshot != stringValue(proposal["baseline_snapshot"]) {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "Host maintenance evidence does not match the ready proposal."
	}
	expires, err := time.Parse(time.RFC3339Nano, stringValue(proposal["expires_at"]))
	if err != nil || !time.Now().Before(expires) {
		return "MAINTENANCE_PROPOSAL_STALE", "The maintenance proposal has expired."
	}
	baselineState := ledger.CaptureBaseline(root, nil, 0)
	entries, clean := baselineState["status_entries"].([]map[string]string)
	if baselineState["status"] != "OK" || baselineState["head"] != baseline || !clean || len(entries) != 0 {
		return "MAINTENANCE_PROPOSAL_STALE", "The checkout no longer matches the clean proposal baseline."
	}
	if stringValue(proposal["baseline_snapshot"]) == "" || snapshot != stringValue(proposal["baseline_snapshot"]) {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The maintenance job baseline does not match its proposal."
	}
	var reviewedSnapshot string
	job.Get("reviewedSnapshot", &reviewedSnapshot)
	if reviewedSnapshot == "" || reviewedSnapshot != snapshot {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The maintenance patch has no matching executed review."
	}
	patch, err := ledger.JobPatch(worktree, snapshot)
	if err != nil {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The reviewed maintenance patch is unavailable."
	}
	digest, err := changedPathsDigest(worktree, snapshot)
	revision, _ := args["revision"].(string)
	var acceptance map[string]any
	job.Get("acceptance", &acceptance)
	if err != nil || digest == "" || digest != stringValue(proposal["changed_paths_sha256"]) || revision != digest || acceptance != nil && acceptance["revision"] != digest {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The maintenance patch no longer matches its reviewed revision."
	}
	reviewPath := stringValue(proposal["review_artifact"])
	if filepath.Dir(reviewPath) != filepath.Join(stateRoot, "bundle_artifacts") {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The maintenance review artifact path is invalid."
	}
	review, err := os.ReadFile(reviewPath)
	if err != nil {
		return "MAINTENANCE_PROPOSAL_STALE", "The maintenance review artifact is missing."
	}
	reviewHash := sha256.Sum256(review)
	var reviewRecord map[string]any
	if hex.EncodeToString(reviewHash[:]) != stringValue(proposal["review_artifact_sha256"]) || json.Unmarshal(review, &reviewRecord) != nil || reviewRecord["changed_paths_sha256"] != digest || hostRun.Revision != digest {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The reviewed artifact does not match the maintenance patch."
	}
	if resolved, _ := args["resolved"].(bool); resolved {
		return "MAINTENANCE_PROPOSAL_CONFLICT", "Maintenance proposals cannot use conflict resolution or replay."
	}
	paths, err := ledger.JobPatchPaths(root, patch)
	proposed, ok := proposal["changed_test_paths"].([]any)
	if err != nil || !ok || !sameStringPaths(paths, proposed) {
		return "MAINTENANCE_PROPOSAL_TAMPERED", "The maintenance patch paths no longer match the proposal."
	}
	return "", ""
}

func sameApplyProjectPath(a, b string) bool {
	normalize := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return ""
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		}
		return filepath.Clean(absolute)
	}
	a, b = normalize(a), normalize(b)
	if a == "" || b == "" {
		return false
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func sameStringPaths(paths []string, values []any) bool {
	if len(paths) != len(values) {
		return false
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		seen[filepath.ToSlash(path)] = true
	}
	for _, value := range values {
		path, ok := value.(string)
		if !ok || !seen[filepath.ToSlash(path)] {
			return false
		}
		delete(seen, filepath.ToSlash(path))
	}
	return len(seen) == 0
}

func changedPathsDigest(worktree, snapshot string) (string, error) {
	return ledger.JobChangedPathsSHA256(worktree, snapshot)
}

func applyMaintenancePatch(root, patch string) map[string]any {
	for _, args := range [][]string{{"apply", "--check", "--binary", "-"}, {"apply", "--binary", "-"}} {
		command := proc.Hide(exec.Command("git", append([]string{"-C", root, "-c", "core.longpaths=true"}, args...)...))
		proc.ConfigureNoWindow(command)
		command.Stdin = strings.NewReader(patch)
		if _, err := command.Output(); err != nil {
			return map[string]any{"applied": false, "conflicts": []string{}, "applicable": []string{}}
		}
	}
	return map[string]any{"applied": true, "conflicts": []string{}}
}

func persistApplied(stateRoot, dir, jobID string, job *Record, value map[string]any, args map[string]any, opt ApplyOptions, applyReason string) map[string]any {
	_ = job.Set("applyState", "applied")
	_ = job.Set("applyResult", value)
	_ = job.Set("appliedAt", float64(time.Now().UnixNano())/1e9)
	if applyReason != "" {
		_ = job.Set("applyReason", applyReason)
	}
	acceptedApply := applyReason == "" || applyReason == "hand_applied_detected"
	if acceptedApply {
		if task, _ := recordMap(job)["taskId"].(string); task != "" {
			_ = job.Set("decisionRecorded", true)
		}
	}
	if err := Write(filepath.Join(dir, jobID+".json"), job); err != nil {
		return applyToolResult(map[string]any{"state": "FAILED", "errorCode": "JOB_WRITE_FAILED", "message": err.Error()})
	}
	var worktree string
	job.Get("worktreePath", &worktree)
	if applyReason == "" {
		recordScopeOutcome(opt, stateRoot, recordMap(job), "applied")
	}
	if acceptedApply {
		recordApplyDecision(opt, stateRoot, recordMap(job), "accepted", "apply", jobID, args["reviewer1Valid"], args["reviewer1False"])
	}
	if err := recordPlanApply(stateRoot, recordMap(job), jobID); err != nil {
		value["plan_warning"] = err.Error()
	}
	enqueueAppliedValidation(stateRoot, recordMap(job), value)
	addReadyPlanMilestones(stateRoot, recordMap(job), value)
	releaseAppliedJobLeases(opt, stateRoot, dir, worktree)
	dispatchWorktreeRetirement(stateRoot, jobID)
	supersedeEarlierTaskJobs(stateRoot, jobID, recordMap(job))
	return applyToolResult(value)
}

func recordApplyObservation(stateRoot string, job, value map[string]any) {
	taskID, _ := job["taskId"].(string)
	jobID, _ := job["jobId"].(string)
	repository, _ := job["repoRoot"].(string)
	paths, _ := value["paths"].([]string)
	if taskID == "" || jobID == "" || repository == "" || value["applied"] != true {
		return
	}
	payload := map[string]any{"job_id": jobID, "applied_paths_count": len(paths)}
	if head, ok := job["head"].(string); ok && head != "" {
		payload["base_head"] = head
	}
	if validation, ok := value["validation"].(map[string]any); ok {
		if status, ok := validation["status"].(string); ok && status != "" {
			payload["validation_status"] = status
		}
	}
	_ = enforce.EmitApplyObservation(stateRoot, repository, taskID, payload)
}

func addReadyPlanMilestones(stateRoot string, job, value map[string]any) {
	repository, _ := job["repoRoot"].(string)
	taskID, _ := job["taskId"].(string)
	if repository == "" || taskID == "" {
		return
	}
	delete(value, "ready_milestones")
	ready, err := contextio.PlanReadiness(stateRoot, repository, taskID)
	if err == nil && len(ready) > 0 {
		value["ready_milestones"] = ready
	}
}

func supersedeEarlierTaskJobs(stateRoot, appliedID string, applied map[string]any) {
	task, _ := applied["taskId"].(string)
	acceptedAt, ok := applied["acceptedAt"].(float64)
	if task == "" || !ok {
		return
	}
	dir := filepath.Join(stateRoot, "codex_exec_mcp", "jobs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if entry.IsDir() || id == appliedID || entry.Name() != id+".json" {
			continue
		}
		lock, err := AcquireOwnership(dir, id)
		if err != nil {
			continue
		}
		prior, err := Read(filepath.Join(dir, entry.Name()))
		if err == nil {
			m := recordMap(prior)
			priorAt, _ := m["acceptedAt"].(float64)
			if m["state"] == "SUCCEEDED" && m["applyState"] != "applied" && m["taskId"] == task && priorAt < acceptedAt {
				_ = prior.Set("state", "SUPERSEDED")
				_ = prior.Set("finishedAt", float64(time.Now().UnixNano())/1e9)
				_ = prior.Set("supersededBy", appliedID)
				if Write(filepath.Join(dir, entry.Name()), prior) == nil {
					_ = lease.ReleaseJobScopeLease(stateRoot, id)
					dispatchWorktreeRetirement(stateRoot, id)
				}
			}
		}
		_ = lock.Release()
	}
}

func recordPlanApply(stateRoot string, job map[string]any, jobID string) error {
	repository, _ := job["repoRoot"].(string)
	taskID, _ := job["taskId"].(string)
	if repository == "" || taskID == "" {
		return nil
	}
	if err := contextio.RecordPlanApply(stateRoot, repository, taskID, jobID); err != nil {
		fmt.Fprintf(os.Stderr, "RecordPlanApply failed for job %s: %v\n", jobID, err)
		return err
	}
	return nil
}

func applyToolResult(value map[string]any) map[string]any {
	var text string
	b, err := json.Marshal(value)
	if err == nil {
		text = string(b)
	}
	_, isError := value["errorCode"]
	if accepted, ok := value["accepted"].(bool); ok && !accepted {
		isError = true
	}
	if applied, ok := value["applied"].(bool); ok && !applied {
		isError = true
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "structuredContent": value, "isError": isError}
}

func patchPaths(root, patch string) ([]string, error) { return ledger.JobPatchPaths(root, patch) }
