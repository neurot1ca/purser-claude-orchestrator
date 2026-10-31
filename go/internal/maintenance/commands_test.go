package maintenance

import (
	"purser-hook/internal/ledger"
	"purser-hook/internal/testutil"
)

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMarkDoneTestGapsRequiresAppliedValidatedSingleCommit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		validation string
		commit     string
		wantDone   bool
	}{
		{name: "validation failed", validation: "failed", commit: "correct"},
		{name: "validation unknown", validation: "unknown", commit: "correct"},
		{name: "missing commit", validation: "passed", commit: "missing"},
		{name: "extra file", validation: "passed", commit: "extra"},
		{name: "wrong parent", validation: "passed", commit: "wrong-parent"},
		{name: "different committed content", validation: "passed", commit: "different-content"},
		{name: "correct commit", validation: "passed", commit: "correct", wantDone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, state, noticePath, readyPath := testGapCompletionFixture(t, tc.validation, tc.commit)
			_, err := Control(state, "mark-done", repo, "", noticePath, nil)
			if tc.wantDone {
				if err != nil {
					t.Fatalf("mark-done: %v", err)
				}
				if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
					t.Fatalf("ready proposal still exists: %v", err)
				}
				if _, err := Control(state, "mark-done", repo, "", noticePath, nil); err != nil {
					t.Fatalf("repeat mark-done: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "pending") {
				t.Fatalf("mark-done error = %v, want pending", err)
			}
			if tc.commit == "different-content" && !strings.Contains(err.Error(), "content does not match") {
				t.Fatalf("mark-done error = %v, want committed content mismatch", err)
			}
			if _, statErr := os.Stat(readyPath); statErr != nil {
				t.Fatalf("pending proposal was removed: %v", statErr)
			}
		})
	}
}

func TestMarkDoneTestGapsRecoversPersistedCompletion(t *testing.T) {
	repo, state, noticePath, readyPath := testGapCompletionFixture(t, "passed", "correct")
	priorRemove := removeCompletedTestProposal
	t.Cleanup(func() { removeCompletedTestProposal = priorRemove })
	removeCompletedTestProposal = func(string) error { return errors.New("interrupted before removal") }
	if _, err := Control(state, "mark-done", repo, "", noticePath, nil); err == nil {
		t.Fatal("expected interruption before ready removal")
	}
	completionPath := filepath.Join(filepath.Dir(readyPath), "completed.json")
	before, err := os.ReadFile(completionPath)
	if err != nil {
		t.Fatalf("completion was not persisted before removal: %v", err)
	}
	var completion map[string]any
	if err := json.Unmarshal(before, &completion); err != nil {
		t.Fatal(err)
	}
	head, err := maintenanceCommandGit(repo, "rev-parse", "HEAD")
	id, _ := completion["job_id"].(string)
	revision, _ := completion["revision"].(string)
	at, _ := completion["completed_at"].(string)
	_, timeErr := time.Parse(time.RFC3339Nano, at)
	if err != nil || completion["commit"] != strings.TrimSpace(head) || id != strings.Repeat("a", 64) || revision == "" || timeErr != nil {
		t.Fatalf("invalid completion record: %s (HEAD error: %v)", before, err)
	}
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatalf("interrupted removal lost ready proposal: %v", err)
	}
	removeCompletedTestProposal = priorRemove
	if _, err := Control(state, "mark-done", repo, "", noticePath, nil); err != nil {
		t.Fatalf("recover before removal: %v", err)
	}
	if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
		t.Fatalf("recovery did not remove ready proposal: %v", err)
	}
	// Also recover the later crash window: removal succeeded but the notice
	// update was not persisted.
	writeTestJSON(t, noticePath, map[string]any{"findings": []any{}})
	if _, err := Control(state, "mark-done", repo, "", noticePath, nil); err != nil {
		t.Fatalf("recover after removal: %v", err)
	}
	after, err := os.ReadFile(completionPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("retry rewrote completion record: %v", err)
	}
}

func testGapCompletionFixture(t *testing.T, validation, commitKind string) (repo, state, noticePath, readyPath string) {
	t.Helper()
	repo, state = t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := testutil.Hide(exec.Command("git", append([]string{"-C", repo}, args...)...))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "base.txt")
	git("commit", "-qm", "baseline")
	baseline := git("rev-parse", "HEAD")
	if commitKind == "wrong-parent" {
		if err := os.WriteFile(filepath.Join(repo, "intermediate.txt"), []byte("intermediate\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "intermediate.txt")
		git("commit", "-qm", "intermediate")
	}
	if commitKind != "missing" {
		if err := os.WriteFile(filepath.Join(repo, "added_test.go"), []byte("package fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "added_test.go")
		if commitKind == "extra" {
			if err := os.WriteFile(filepath.Join(repo, "extra.txt"), []byte("extra\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", "extra.txt")
		}
		git("commit", "-qm", "maintenance tests")
	}
	root, err := CanonicalRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	digest, err := ledger.JobChangedPathsSHA256(root, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if commitKind == "different-content" {
		if err := os.WriteFile(filepath.Join(repo, "added_test.go"), []byte("package unvalidated\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "added_test.go")
		git("commit", "--amend", "--no-edit", "-q")
		// Restoring validated bytes in the checkout must not hide bad HEAD bytes.
		if err := os.WriteFile(filepath.Join(repo, "added_test.go"), []byte("package fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	jobPath := filepath.Join(state, "codex_exec_mcp", "jobs", id+".json")
	if err := os.MkdirAll(filepath.Dir(jobPath), 0700); err != nil {
		t.Fatal(err)
	}
	job := map[string]any{"jobId": id, "repoRoot": root, "worktreePath": filepath.Join(state, "worktrees", id), "maintenance_test_job": true, "maintenance_job_id": id, "maintenance_identity_source": "host", "applyState": "applied", "appliedRevision": digest}
	writeTestJSON(t, jobPath, job)
	keyHash := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	validationPath := filepath.Join(state, "apply_validation", fmt.Sprintf("%x", keyHash), id+".json")
	if err := os.MkdirAll(filepath.Dir(validationPath), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, validationPath, map[string]any{"job_id": id, "repository": root, "revision": digest, "maintenance_test_job": true, "status": validation})
	reports := filepath.Join(MaintenanceDirectory(state, root), "reports", "test-gaps")
	readyPath, noticePath = filepath.Join(reports, "ready.json"), filepath.Join(reports, "notice.notice.json")
	if err := os.MkdirAll(reports, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, readyPath, map[string]any{"status": "ready", "job_id": id, "project_root": root, "baseline_head": baseline, "changed_paths_sha256": digest, "changed_test_paths": []string{"added_test.go"}})
	writeTestJSON(t, noticePath, map[string]any{"findings": []any{}})
	return repo, state, noticePath, readyPath
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDelayedCheckUsesPostDelayActivityAndQuota(t *testing.T) {
	oldSleep, oldClock := checkSleep, maintenanceClock
	t.Cleanup(func() { checkSleep, maintenanceClock = oldSleep, oldClock })

	started := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	finished := started.Add(6 * time.Minute)
	checkSleep = func(time.Duration) {}
	maintenanceClock = func() time.Time { return finished }

	repo, state, quotaHome := t.TempDir(), t.TempDir(), t.TempDir()
	if out, err := testutil.Hide(exec.Command("git", "-C", repo, "init", "-q")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	root, err := CanonicalRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	directory := MaintenanceDirectory(state, root)
	for path, value := range map[string]any{
		filepath.Join(directory, "opt-in.json"):        map[string]any{"enabled": true, "project_root": root},
		filepath.Join(directory, "last-activity.json"): map[string]any{"at": float64(started.Unix())},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", quotaHome)
	quotaPath := filepath.Join(quotaHome, "sessions", finished.Format("2006"), finished.Format("01"), finished.Format("02"), "rollout-quota.jsonl")
	quotaEvent, _ := json.Marshal(map[string]any{
		"timestamp": finished.Format(time.RFC3339),
		"payload": map[string]any{"rate_limits": map[string]any{
			"primary": map[string]any{"window_minutes": 300, "used_percent": 1.0, "resets_at": float64(finished.Add(time.Hour).Unix())},
		}},
	})
	if err := os.MkdirAll(filepath.Dir(quotaPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quotaPath, append(quotaEvent, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	_, err = DelayedCheck(root, state, 5*time.Minute, Runner{})
	if err == nil || !strings.Contains(err.Error(), "maintenance job operations are required") {
		t.Fatalf("DelayedCheck error = %v, want it to reach report execution after the quiet period", err)
	}
	if _, err := os.Stat(filepath.Join(state, "maintenance", "active.json")); err == nil {
		t.Fatal("check unexpectedly acquired the global maintenance slot")
	}
}

func TestDelayedCheckKeepsCodexQuotaGate(t *testing.T) {
	oldSleep, oldClock := checkSleep, maintenanceClock
	t.Cleanup(func() { checkSleep, maintenanceClock = oldSleep, oldClock })
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	checkSleep = func(time.Duration) {}
	maintenanceClock = func() time.Time { return now }
	repo, state := t.TempDir(), t.TempDir()
	if out, err := testutil.Hide(exec.Command("git", "-C", repo, "init", "-q")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	root, err := CanonicalRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	directory := MaintenanceDirectory(state, root)
	for path, value := range map[string]any{
		filepath.Join(directory, "opt-in.json"):        map[string]any{"enabled": true, "project_root": root},
		filepath.Join(directory, "last-activity.json"): map[string]any{"at": float64(now.Add(-6 * time.Minute).Unix())},
	} {
		data, _ := json.Marshal(value)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeJobs{}
	_, err = DelayedCheck(root, state, 0, Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "gpt-6-luna", CoderEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if fake.started != nil {
		t.Fatalf("Codex maintenance started without fresh quota: %#v", fake.started)
	}
	openCode := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED", "content": "```purser-findings-json\n[]\n```"}}}
	_, err = DelayedCheck(root, state, 0, Runner{Jobs: openCode, CoderAgent: "opencode", CoderModel: "deepseek/deepseek-v4-pro", CoderEffort: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if openCode.started["agent"] != "opencode" {
		t.Fatalf("OpenCode maintenance did not run without a quota snapshot: %#v", openCode.started)
	}
}
