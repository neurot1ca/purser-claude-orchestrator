package enforce

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"purser-hook/internal/fsretry"
)

const reviewDiffInlineLimit = 12 * 1024
const reviewDiffResponseLimit = 24 * 1024

func attachCompleteReviewDiff(root string, compact, bundle map[string]any) {
	if bundle["phase"] != "implementation" || compact["claude_review_mode"] != "FULL" || numberOr(bundle["delta_files"], -1) == 0 {
		return
	}
	patch, ok := bundle["_complete_review_patch"].(string)
	revision := stringValue(bundle["_complete_review_revision"])
	if bundle["_complete_review_requested"] != true {
		if _, presented := bundle["review_diff_complete"]; !presented {
			return
		}
		patch, ok = bundle["review_diff_inline"].(string)
		revision = stringValue(bundle["review_diff_revision"])
		if !ok && bundle["review_diff_complete"] == true {
			var raw []byte
			readErr := fsretry.Retry(func() (err error) {
				raw, err = os.ReadFile(stringValue(bundle["review_diff_path"]))
				return err
			}, 2*time.Second)
			patch, ok = string(raw), readErr == nil
		}
	}
	if !ok || revision == "" {
		compact["review_diff_complete"] = false
		compact["review_diff_notice"] = "Review diff unavailable; complete review is still required."
		return
	}
	patchBytes := []byte(patch)
	sum := sha256.Sum256(patchBytes)
	if hex.EncodeToString(sum[:]) != revision || revision != stringValue(bundle["changed_paths_sha256"]) {
		compact["review_diff_complete"] = false
		compact["review_diff_notice"] = "Review diff unavailable; complete review is still required."
		return
	}
	path, err := filepath.Abs(filepath.Join(root, "review_diffs", revision+".diff"))
	if err != nil || atomicWrite(path, patchBytes) != nil {
		compact["review_diff_complete"] = false
		compact["review_diff_notice"] = "Review diff unavailable; complete review is still required."
		return
	}
	base := map[string]any{"review_diff_complete": true, "review_diff_bytes": len(patchBytes), "review_diff_revision": revision}
	if len(patchBytes) <= reviewDiffInlineLimit && utf8.Valid(patchBytes) && inlineReviewPatchSupported(patch) {
		for key, value := range base {
			compact[key] = value
		}
		compact["review_diff_inline"] = patch
		compact["review_diff_notice"] = "Complete review diff is inline."
		if encoded, encodeErr := canonicalJSON(compact); encodeErr == nil && len(encoded) <= reviewDiffResponseLimit {
			return
		}
		delete(compact, "review_diff_inline")
	}
	for key, value := range base {
		compact[key] = value
	}
	compact["review_diff_path"] = path
	compact["review_diff_notice"] = fmt.Sprintf("Complete review diff is in `%s` (`%d` bytes; revision `%s`). Read it before accepting.", path, len(patchBytes), revision)
}

func inlineReviewPatchSupported(patch string) bool {
	for _, marker := range []string{"GIT binary patch", "Binary files ", "old mode ", "new mode ", "deleted file mode ", "new file mode ", "rename from ", "rename to ", "copy from ", "copy to "} {
		if strings.Contains(patch, marker) {
			return false
		}
	}
	return true
}

var planSummaryHeading = regexp.MustCompile(`(?im)^#{0,6}\s*Summary:\s*(.*)$`)
var planNextHeading = regexp.MustCompile(`(?m)^(#{1,6})\s+\S`)

func storePostPlan(root string, event map[string]any, taskID any, report string, bundle map[string]any) (string, error) {
	name := fmt.Sprint(taskID)
	if taskID == nil || name == "" || name == "<nil>" || taskID == false || taskID == 0 || taskID == float64(0) {
		name = "task"
	}
	name = regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(name, "_")
	if utf8.RuneCountInString(name) > 80 {
		name = string([]rune(name)[:80])
	}
	key, err := callKey(event)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, "plans", name+"-"+key+".md"))
	if err != nil {
		return "", err
	}
	content := report
	if strings.TrimSpace(report) == "" {
		bundle["findings"] = appendFinding(bundle["findings"], "PLAN_OUTPUT_EMPTY", "Plan output is empty.")
	}
	missing := make([]string, 0, 7)
	for _, heading := range []string{"Summary:", "Goal and boundaries", "Current evidence", "Decision", "Milestones", "Risks/open decisions", "Implementation handoff"} {
		if !planHasHeading(report, heading) {
			missing = append(missing, heading)
		}
	}
	if len(missing) > 0 {
		bundle["findings"] = appendFinding(bundle["findings"], "PLAN_SHAPE_MISSING", "Plan lacks the required sections: "+strings.Join(missing, ", ")+".")
	}
	if err := atomicWrite(path, []byte(content)); err != nil {
		return "", err
	}
	summary := postPlanSummary(content)
	if summary == "" && strings.TrimSpace(content) != "" {
		summary = "Plan summary is empty; retrieve the complete plan with codex_result artifact=\"plan\"."
	}
	bundle["plan_path"], bundle["plan_summary"] = path, summary
	return path, nil
}

func planHasHeading(report, heading string) bool {
	pattern := regexp.MustCompile(`(?im)^#{0,6}\s*` + regexp.QuoteMeta(heading) + `(?:\s|:|$)`)
	return pattern.MatchString(report)
}

func appendFinding(value any, code, message string) []any {
	findings, ok := value.([]any)
	if !ok {
		findings = []any{}
	}
	return append(findings, map[string]any{"code": code, "message": message})
}

func postPlanSummary(report string) string {
	match := planSummaryHeading.FindStringSubmatchIndex(report)
	if match == nil {
		return report
	}
	tail := report[match[2]:]
	level := len(strings.TrimSpace(report[match[0]:match[1]])) - len(strings.TrimLeft(strings.TrimSpace(report[match[0]:match[1]]), "#"))
	for _, next := range planNextHeading.FindAllStringSubmatchIndex(tail, -1) {
		nextLevel := next[3] - next[2]
		if level == 0 || nextLevel <= level {
			tail = tail[:next[0]]
			break
		}
	}
	return strings.TrimSpace(tail)
}

func prefixRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func postReportLimit(config map[string]any, phase any) int {
	key, fallback := "implementation_report_max_chars", 1200
	if phase == "operation" {
		key, fallback = "operation_report_max_chars", 24000
	}
	switch value := config[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		if parsed, err := value.Float64(); err == nil {
			return int(parsed)
		}
	default:
		return fallback
	}
	return fallback
}

// finalizePostArtifacts adds the quota snapshot within the compact-response byte cap.
// The complete bundle is persisted by finalizeBundle before its response is compacted.
func finalizePostArtifacts(root string, event, bundle map[string]any, cap, reportLimit int) (map[string]any, string, error) {
	// Python persists the original bundle, before response-only derived fields
	// and validation-line compaction are applied.
	artifactBundle := make(map[string]any, len(bundle))
	for key, value := range bundle {
		artifactBundle[key] = value
	}
	delete(artifactBundle, "delta")
	delete(artifactBundle, "force_full_review")
	artifactBundle["changed_path_count"] = nil
	_, path, err := finalizeBundle(root, event, artifactBundle, cap, reportLimit)
	if err != nil {
		return nil, "", err
	}
	bundle["full_bundle_path"] = artifactBundle["full_bundle_path"]
	// finalizeBundle may have promoted the bundle when its mandatory fields
	// exceeded the response cap. Keep that decision in the caller's bundle.
	bundle["claude_review_mode"] = artifactBundle["claude_review_mode"]
	bundle["audit_receipt"] = artifactBundle["audit_receipt"]
	if metadata, ok := artifactBundle["review_navigation"]; ok {
		bundle["review_navigation"] = metadata
	}
	delete(bundle, "_review_diff")
	compact, _, err := compactBundle(bundle, cap)
	if err != nil {
		return nil, "", err
	}
	delete(compact, "delta")
	delete(compact, "force_full_review")
	stripWorkerValidation(compact)
	if report, ok := compact["implementer_report"].(string); ok {
		compact["implementer_report"] = prefixRunes(report, reportLimit)
	}
	if quota, exists := bundle["codex_quota"]; exists {
		compact["codex_quota"] = quota
		size, sizeErr := nonReportBytes(compact)
		if sizeErr != nil {
			return nil, "", sizeErr
		}
		if excess := size - cap; excess > 0 {
			smaller, overflow, compactErr := compactBundle(bundle, maxInt(0, cap-excess))
			if compactErr != nil {
				return nil, "", compactErr
			}
			smaller["codex_quota"] = quota
			if size, compactErr = nonReportBytes(smaller); compactErr != nil {
				return nil, "", compactErr
			} else if !overflow && size <= cap {
				compact = smaller
				stripWorkerValidation(compact)
				if report, ok := compact["implementer_report"].(string); ok {
					compact["implementer_report"] = prefixRunes(report, reportLimit)
				}
			} else {
				delete(compact, "codex_quota")
			}
		}
	}
	applyReviewNavigationPreview(compact, bundle)
	attachCompleteReviewDiff(root, compact, bundle)
	presentation := false
	for key := range bundle {
		if strings.HasPrefix(key, "_complete_review_") {
			delete(bundle, key)
		}
		if strings.HasPrefix(key, "review_diff_") {
			delete(bundle, key)
			delete(artifactBundle, key)
			presentation = true
		}
	}
	for key, value := range compact {
		if strings.HasPrefix(key, "review_diff_") {
			bundle[key], artifactBundle[key] = value, value
			presentation = true
		}
	}
	if presentation {
		encoded, encodeErr := canonicalJSON(artifactBundle)
		if encodeErr != nil {
			return nil, "", encodeErr
		}
		if err = atomicWrite(path, encoded); err != nil {
			return nil, "", err
		}
	}
	return compact, path, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
