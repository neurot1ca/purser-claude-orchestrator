package jobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/ledger"
)

func TestApplyMaintenanceProposalFailuresWriteNothing(t *testing.T) {
	for _, mode := range []string{"stale-head", "dirty-checkout", "tampered-patch", "expired"} {
		t.Run(mode, func(t *testing.T) {
			id := strings.Repeat("d", 64)
			state, repo, worktree, snapshot := acceptanceJob(t, filepath.Join(t.TempDir(), "state"), id)
			write(t, filepath.Join(worktree, "added_test.go"), "package fixture\nfunc TestAdded(t *testing.T) {}\n")
			git(t, worktree, "add", "added_test.go")
			revision, err := changedPathsDigest(worktree, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			jobPath := filepath.Join(state, "codex_exec_mcp", "jobs", id+".json")
			job, err := Read(jobPath)
			if err != nil {
				t.Fatal(err)
			}
			baselineHead := strings.TrimSpace(string(gitOutput(t, repo, "rev-parse", "HEAD")))
			for key, value := range map[string]any{"maintenance_test_job": true, "maintenance_identity_source": "host", "maintenance_job_id": id} {
				if err = job.Set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			if err = job.Set("maintenanceTests", MaintenanceTestRun{JobID: id, BaselineHead: baselineHead, BaselineSnapshot: snapshot, Revision: revision}); err != nil {
				t.Fatal(err)
			}
			if err = Write(jobPath, job); err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(state, "projects", contextio.ProjectKey(repo), "maintenance", "reports", "test-gaps", "ready.json")
			reviewPath := filepath.Join(state, "bundle_artifacts", "review.json")
			reviewData, _ := json.Marshal(map[string]any{"changed_paths_sha256": revision})
			write(t, reviewPath, string(reviewData))
			reviewHash := sha256.Sum256(reviewData)
			proposal := map[string]any{"status": "ready", "job_id": id, "project_root": repo, "baseline_head": baselineHead, "baseline_snapshot": snapshot,
				"changed_paths_sha256": revision, "changed_test_paths": []string{"added_test.go"}, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), "review_artifact": reviewPath, "review_artifact_sha256": hex.EncodeToString(reviewHash[:])}
			if mode == "tampered-patch" {
				proposal["changed_paths_sha256"] = strings.Repeat("0", 64)
			}
			if mode == "expired" {
				proposal["expires_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
			}
			data, _ := json.Marshal(proposal)
			write(t, ready, string(data))
			switch mode {
			case "stale-head":
				write(t, filepath.Join(repo, "other.txt"), "new\n")
				git(t, repo, "add", "other.txt")
				git(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "drift")
			case "dirty-checkout":
				write(t, filepath.Join(repo, "tracked.txt"), "dirty\n")
			}
			beforeJob, _ := os.ReadFile(jobPath)
			beforeReady, _ := os.ReadFile(ready)
			beforeSnapshot, _ := ledger.CheckoutSnapshot(repo)
			response := Apply(state, map[string]any{"jobId": id, "revision": revision})
			result := response["structuredContent"].(map[string]any)
			if response["isError"] != true || result["errorCode"] == nil {
				t.Fatalf("expected fail-closed result: %#v", response)
			}
			afterJob, _ := os.ReadFile(jobPath)
			afterReady, _ := os.ReadFile(ready)
			afterSnapshot, err := ledger.CheckoutSnapshot(repo)
			if err != nil || !bytes.Equal(beforeJob, afterJob) || !bytes.Equal(beforeReady, afterReady) || beforeSnapshot != afterSnapshot {
				t.Fatalf("failed apply changed state: job=%v proposal=%v checkout=%v err=%v", !bytes.Equal(beforeJob, afterJob), !bytes.Equal(beforeReady, afterReady), beforeSnapshot != afterSnapshot, err)
			}
		})
	}
}
