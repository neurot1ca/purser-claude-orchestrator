package enforce

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/review"
)

func TestMaintenanceReviewRequiresHostIdentityAndExecutedReview(t *testing.T) {
	base := func() (map[string]any, map[string]any) {
		pending := map[string]any{"phase": "implementation", "route": "codex-main", "task_contract_grounded": true,
			"maintenance_test_job": true, "maintenance_identity_source": "host", "maintenance_job_id": "job-1", "job_id": "job-1"}
		bundle := baseBundle("")
		bundle["delta"] = map[string]any{"status": "OK", "changed_paths": []string{"pkg/x_test.go"}, "changed_file_count": 1, "changed_line_count": 1}
		bundle["thread_id"] = "thread"
		bundle["worker_validation"] = map[string]any{"status": "PASS"}
		return pending, bundle
	}
	pending, bundle := base()
	delete(pending, "maintenance_identity_source")
	schedulePostReview(t.TempDir(), pending, bundle, postProfile{}, nil, map[string]any{"small_delta_max_files": 10, "small_delta_max_lines": 20}, time.Second, true, true)
	if bundle["reviewer1_skipped_reason"] != "MAINTENANCE_IDENTITY_INVALID" || bundle["reviewer1_result"] != nil {
		t.Fatalf("invalid identity review state = %#v", bundle)
	}

	pending, bundle = base()
	schedulePostReview(t.TempDir(), pending, bundle, postProfile{}, nil, map[string]any{"small_delta_max_files": 10, "small_delta_max_lines": 20}, time.Second, true, true)
	if bundle["claude_review_mode"] != review.Full || bundle["reviewer1_skipped_reason"] != nil {
		t.Fatalf("small maintenance delta did not require reviewer execution: %#v", bundle)
	}

	pending, bundle = base()
	pending["phase"] = "plan"
	schedulePostReview(t.TempDir(), pending, bundle, postProfile{}, nil, map[string]any{"small_delta_max_files": 10, "small_delta_max_lines": 20}, time.Second, true, true)
	if bundle["reviewer1_skipped_reason"] != review.PhasePlan || bundle["reviewer1_result"] != nil || bundle["claude_review_mode"] != review.Full {
		t.Fatalf("maintenance override replaced plan review gate: %#v", bundle)
	}

	pending, bundle = base()
	schedulePostReview(t.TempDir(), pending, bundle, postProfile{Reviewer: "none"}, nil, map[string]any{"small_delta_max_files": 10, "small_delta_max_lines": 20}, time.Second, true, true)
	if bundle["reviewer1_skipped_reason"] != "REVIEWER_DISABLED" || bundle["reviewer1_result"] != nil || bundle["claude_review_mode"] != review.Full {
		t.Fatalf("disabled reviewer was accepted: %#v", bundle)
	}
}

func TestMaintenanceReviewLeavesOrdinaryThresholdUnchanged(t *testing.T) {
	bundle := baseBundle("")
	bundle["delta"] = map[string]any{"status": "OK", "changed_paths": []string{"pkg/x.go"}, "changed_file_count": 1, "changed_line_count": 1}
	bundle["thread_id"] = "thread"
	bundle["worker_validation"] = map[string]any{"status": "PASS"}
	pending := map[string]any{"phase": "implementation", "route": "codex-main", "task_contract_grounded": true}
	schedulePostReview(t.TempDir(), pending, bundle, postProfile{}, nil, map[string]any{"small_delta_max_files": 10, "small_delta_max_lines": 20}, time.Second, true, true)
	if bundle["claude_review_mode"] != review.Audit || bundle["reviewer1_skipped_reason"] != review.BelowThreshold {
		t.Fatalf("ordinary small-delta policy changed: %#v", bundle)
	}
}

func TestMaintenanceJobsStayOffOrdinaryBoard(t *testing.T) {
	root := t.TempDir()
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	write := func(path string, value any) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("async_jobs/ordinary.json", map[string]any{"project_key": "p", "jobId": id, "call": "c"})
	write("codex_exec_mcp/jobs/"+id+".json", map[string]any{"project_key": "p", "jobId": id, "call": "c", "state": "QUEUED", "label": "ordinary"})
	id2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	write("async_jobs/private.json", map[string]any{"project_key": "p", "jobId": id2, "call": "d", "maintenance_test_job": true})
	write("codex_exec_mcp/jobs/"+id2+".json", map[string]any{"project_key": "p", "jobId": id2, "call": "d", "state": "QUEUED", "label": "private", "maintenance_test_job": true})
	board, ids := PostProjectJobBoard(root, "p", time.Unix(1800000000, 0))
	if len(ids) != 1 || ids[0] != id || !strings.Contains(board, "ordinary") || strings.Contains(board, "private") {
		t.Fatalf("board leaked or hid jobs: ids=%v board=%q", ids, board)
	}
}
