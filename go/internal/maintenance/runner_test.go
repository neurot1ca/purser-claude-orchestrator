package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/jobs"
	"purser-hook/internal/lease"
	"purser-hook/internal/ledger"
	"purser-hook/internal/proc"
	"purser-hook/internal/profile"
	"purser-hook/internal/testcoverage"
)

type fakeJobs struct {
	state       string
	jobID       string
	markerJobID string
	started     map[string]any
	results     []map[string]any
	resultCalls int
	cancel      map[string]any
	cancelCalls int
	onStart     func(string)
	onResult    func()
	finalize    func(context.Context, string, string, map[string]any) error
	waitStart   bool
}

func (f *fakeJobs) Start(ctx context.Context, state string, args map[string]any) (map[string]any, error) {
	f.state = state
	f.started = args
	if f.jobID == "" {
		f.jobID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	if f.onStart != nil {
		f.onStart(state)
	}
	if f.waitStart {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return map[string]any{"structuredContent": map[string]any{"jobId": f.jobID, "state": "RUNNING"}}, nil
}
func (f *fakeJobs) Result(_, _ string) map[string]any {
	if f.onResult != nil {
		f.onResult()
		f.onResult = nil
	}
	markers, _ := filepath.Glob(filepath.Join(f.state, "projects", "*", "maintenance", "cleanup-active.json"))
	if len(markers) > 0 {
		if data, err := os.ReadFile(markers[0]); err == nil {
			var marker map[string]any
			_ = json.Unmarshal(data, &marker)
			f.markerJobID, _ = marker["job_id"].(string)
		}
	}
	index := f.resultCalls
	f.resultCalls++
	if index >= len(f.results) {
		index = len(f.results) - 1
	}
	if index < 0 {
		return map[string]any{"state": "RUNNING"}
	}
	return map[string]any{"structuredContent": f.results[index]}
}
func (f *fakeJobs) Cancel(_, _ string) map[string]any {
	f.cancelCalls++
	return map[string]any{"structuredContent": f.cancel}
}

func (f *fakeJobs) Finalize(ctx context.Context, state, id string, result map[string]any) error {
	if f.finalize != nil {
		return f.finalize(ctx, state, id, result)
	}
	return errors.New("fixture has no executed reviewer")
}

func TestLifecycleOracle(t *testing.T) {
	path := filepath.Join("testdata", "maintenance_reports_oracle.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		LifecycleCases []struct {
			Name              string           `json:"name"`
			Results           []map[string]any `json:"results"`
			UserJob           bool             `json:"user_job"`
			CancelState       string           `json:"cancel_state"`
			WantError         string           `json:"want_error"`
			WantResultState   string           `json:"want_result_state"`
			WantCancelCalls   int              `json:"want_cancel_calls"`
			WantSlotRetained  bool             `json:"want_slot_retained"`
			WantActiveRemoved bool             `json:"want_active_removed"`
			WantMarkerJobID   bool             `json:"want_marker_job_id"`
		} `json:"lifecycle_cases"`
	}
	if err := json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	if len(oracle.LifecycleCases) != 5 {
		t.Fatalf("expected five lifecycle cases, got %d", len(oracle.LifecycleCases))
	}
	for _, tc := range oracle.LifecycleCases {
		t.Run(tc.Name, func(t *testing.T) {
			state := t.TempDir()
			jobsDir := filepath.Join(state, "codex_exec_mcp", "jobs")
			fake := &fakeJobs{results: tc.Results, cancel: map[string]any{"state": tc.CancelState}}
			if tc.UserJob {
				fake.onStart = func(_ string) {
					_ = os.MkdirAll(jobsDir, 0700)
					job, _ := json.Marshal(map[string]any{"jobId": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "repoRoot": fake.started["cwd"], "state": "RUNNING"})
					_ = os.WriteFile(filepath.Join(jobsDir, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.json"), job, 0600)
				}
			}
			current := time.Unix(100, 0)
			runner := Runner{Jobs: fake, Now: func() time.Time { return current }, Sleep: func(d time.Duration) { current = current.Add(d) }, PollInterval: time.Second, JobTimeout: 3 * time.Second}
			result, runErr := runner.Run(state, t.TempDir(), "cleanup", "oracle prompt")
			if tc.WantError != "" {
				if runErr == nil || runErr.Error() != tc.WantError {
					t.Fatalf("error = %v, want %q", runErr, tc.WantError)
				}
			} else if runErr != nil {
				t.Fatal(runErr)
			}
			if tc.WantResultState != "" && result["state"] != tc.WantResultState {
				t.Fatalf("result = %#v", result)
			}
			if fake.cancelCalls != tc.WantCancelCalls {
				t.Fatalf("cancel calls = %d", fake.cancelCalls)
			}
			slot := filepath.Join(state, "maintenance", "active.json")
			if got := fileExists(slot); got != tc.WantSlotRetained {
				t.Fatalf("slot exists = %v", got)
			}
			activeMarkers, _ := filepath.Glob(filepath.Join(state, "projects", "*", "maintenance", "cleanup-active.json"))
			if removed := len(activeMarkers) == 0; removed != tc.WantActiveRemoved {
				t.Fatalf("active marker removed = %v, want %v", removed, tc.WantActiveRemoved)
			}
			if tc.WantMarkerJobID && fake.resultCalls > 0 {
				// The marker is removed on exit; verify it held the durable ID during polling via hook below.
				if fake.markerJobID != fake.jobID {
					t.Fatalf("marker job id = %q", fake.markerJobID)
				}
			}
		})
	}
}

func TestRunScopesActiveJobsToRepository(t *testing.T) {
	for _, tc := range []struct {
		name      string
		project   string
		wantError bool
	}{
		{name: "other project running job", project: "other"},
		{name: "this project running job", project: "root", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, root := t.TempDir(), t.TempDir()
			jobsDir := filepath.Join(state, "codex_exec_mcp", "jobs")
			if err := os.MkdirAll(jobsDir, 0700); err != nil {
				t.Fatal(err)
			}
			project := t.TempDir()
			if tc.project == "root" {
				project = root
			}
			content, _ := json.Marshal(map[string]any{"state": "RUNNING", "repoRoot": project})
			if err := os.WriteFile(filepath.Join(jobsDir, "job.json"), content, 0600); err != nil {
				t.Fatal(err)
			}
			fake := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}}
			runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "gpt-6-luna", Sleep: func(time.Duration) {}}
			_, err := runner.Run(state, root, "cleanup", "prompt")
			if tc.wantError {
				if err == nil || err.Error() != "active or unknown user job state; maintenance skipped" {
					t.Fatalf("error = %v", err)
				}
				if fake.started != nil {
					t.Fatal("maintenance started despite this project's active job")
				}
			} else if err != nil {
				t.Fatalf("maintenance failed: %v", err)
			}
		})
	}
}

func TestActiveUserJobsIgnoresOrphanQueuedRecord(t *testing.T) {
	state, root := t.TempDir(), t.TempDir()
	dir := filepath.Join(state, "codex_exec_mcp", "jobs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "orphan"
	data, _ := json.Marshal(map[string]any{"jobId": id, "repoRoot": root, "state": "QUEUED", "acceptedAt": time.Now().Add(-7 * time.Hour).Unix()})
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	active, known := activeUserJobs(state, root, "")
	if active || !known {
		t.Fatalf("activeUserJobs = (%v, %v), want (false, true)", active, known)
	}
}

func TestActiveUserJobsKeepsRecentOrLiveRecord(t *testing.T) {
	if os.Getenv("PURSER_MAINTENANCE_LIVE_HOST") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	for _, tc := range []struct {
		name string
		job  map[string]any
	}{
		{"recent queued", map[string]any{"state": "QUEUED", "acceptedAt": time.Now().Unix()}},
		{"live host", map[string]any{"state": "RUNNING", "acceptedAt": time.Now().Add(-7 * time.Hour).Unix()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, root := t.TempDir(), t.TempDir()
			dir := filepath.Join(state, "codex_exec_mcp", "jobs")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.name == "live host" {
				t.Setenv("PURSER_CODEX_TEST_PARENT_PID", strconv.Itoa(os.Getpid()))
				env := append(os.Environ(), "PURSER_MAINTENANCE_LIVE_HOST=1")
				pid, created, err := jobs.LaunchDetached([]string{os.Args[0], "-test.run=^TestActiveUserJobsKeepsRecentOrLiveRecord$"}, env, root)
				if err != nil {
					t.Fatal(err)
				}
				process, err := os.FindProcess(pid)
				if err != nil {
					t.Fatal(err)
				}
				defer process.Release()
				defer process.Kill()
				tc.job["hostPid"], tc.job["hostCreatedAt"] = pid, created
			}
			tc.job["repoRoot"] = root
			data, _ := json.Marshal(tc.job)
			if err := os.WriteFile(filepath.Join(dir, "job.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			active, known := activeUserJobs(state, root, "")
			if !active || !known {
				t.Fatalf("activeUserJobs = (%v, %v), want (true, true)", active, known)
			}
		})
	}
}

func TestRunStartsConfiguredOpenCodeAgentAndOperationModel(t *testing.T) {
	state, repo := t.TempDir(), t.TempDir()
	fake := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}}
	runner := Runner{Jobs: fake, CoderAgent: "opencode", CoderModel: "deepseek/deepseek-v4-pro", CoderEffort: "none", Sleep: func(time.Duration) {}}
	if _, err := runner.Run(state, repo, "cleanup", "prompt"); err != nil {
		t.Fatal(err)
	}
	if fake.started["agent"] != "opencode" || fake.started["jobModel"] != "deepseek/deepseek-v4-pro" || fake.started["jobEffort"] != "none" || fake.started["jobPhase"] != "operation" || fake.started["sandbox"] != "read-only" {
		t.Fatalf("maintenance start args = %#v", fake.started)
	}
}

func TestRunTestGapsUsesWritableWorktreeAndRequiresCleanUnchangedBaseline(t *testing.T) {
	root := maintenancePatchRepo(t)
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := os.ReadFile(filepath.Join(root, "source.go"))
	if err != nil {
		t.Fatal(err)
	}
	state := maintenanceScratch(t)
	fake := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}}
	fake.onStart = func(state string) { stageMaintenancePatch(t, state, root, fake) }
	fake.finalize = reviewMaintenancePatch
	attempts := 0
	oldRename := renameTestProposal
	renameTestProposal = func(src, dst string) error {
		attempts++
		if attempts == 1 {
			if runtime.GOOS == "windows" {
				locked, err := os.Open(src)
				if err != nil {
					return err
				}
				go func() { time.Sleep(75 * time.Millisecond); _ = locked.Close() }()
				return os.Rename(src, dst)
			}
			return os.ErrPermission
		}
		return os.Rename(src, dst)
	}
	t.Cleanup(func() { renameTestProposal = oldRename })
	runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "gpt-6-luna", Sleep: func(time.Duration) {}}
	if _, err := runner.Run(state, root, "test-gaps", "write tests"); err != nil {
		t.Fatal(err)
	}
	if fake.started["sandbox"] != "workspace-write" || fake.started["jobPhase"] != "implementation" || fake.started["worktree"] != true {
		t.Fatalf("test-gaps job is not a writable implementation worktree: %#v", fake.started)
	}
	if _, err := os.Stat(filepath.Join(root, "source.go")); err != nil {
		t.Fatal(err)
	}
	indexAfter, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatalf("test-gaps lifecycle changed checkout index: %v", err)
	}
	sourceAfter, err := os.ReadFile(filepath.Join(root, "source.go"))
	if err != nil || !bytes.Equal(sourceBefore, sourceAfter) {
		t.Fatalf("test-gaps lifecycle changed checkout source: %v", err)
	}
	readyPath := filepath.Join(MaintenanceDirectory(state, root), "reports", "test-gaps", "ready.json")
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("transient publication retries=%d", attempts)
	}
	if _, err := runner.Run(state, root, "test-gaps", "write tests"); err != nil || fake.resultCalls != 1 {
		t.Fatalf("ready proposal was not reused: %v, calls=%d", err, fake.resultCalls)
	}
	if err := removeTestProposal(readyPath); err != nil {
		t.Fatal(err)
	}
	fake = &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}}
	fake.onResult = func() {
		if output, err := proc.Hide(exec.Command("git", "-C", root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "user work")).CombinedOutput(); err != nil {
			t.Fatalf("move HEAD: %v %s", err, output)
		}
	}
	runner.Jobs = fake
	if _, err := runner.Run(state, root, "test-gaps", "write tests"); err == nil || err.Error() != "test-gaps checkout baseline changed during maintenance" {
		t.Fatalf("moved baseline error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "local-change"), []byte("user change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fake = &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}}
	runner.Jobs = fake
	if _, err := runner.Run(state, root, "test-gaps", "write tests"); err == nil || err.Error() != "test-gaps maintenance requires a clean checkout" {
		t.Fatalf("dirty baseline error = %v", err)
	}
	if fake.started != nil {
		t.Fatal("test-gaps job started from a dirty checkout")
	}
}

func maintenancePatchRepo(t *testing.T) string {
	t.Helper()
	root := maintenanceScratch(t)
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package fixture\nfunc value() int { return 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	commitMaintenanceFixture(t, root)
	return root
}

func commitMaintenanceFixture(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"config", "core.autocrlf", "false"}, {"add", "-A"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "baseline"}} {
		if output, err := proc.Hide(exec.Command("git", append([]string{"-C", root}, args...)...)).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
}

// This fixture supplies host/reviewer records; real coverage execution is tested in jobs.
func stageMaintenancePatch(t *testing.T, state, root string, fake *fakeJobs) {
	t.Helper()
	created, err := ledger.CreateJobWorktree(root, fake.jobID, filepath.Join(state, "wt"))
	if err != nil {
		t.Fatal(err)
	}
	testPath := "source_test.go"
	code := "package fixture\nimport \"testing\"\nfunc TestGenerated(t *testing.T) { if value()!=1 { t.Fatal(\"wrong value\") } }\n"
	if _, err := os.Stat(filepath.Join(root, "shape.go")); err == nil {
		testPath = "shape_more_test.go"
		code = "package shape\nimport \"testing\"\nfunc TestGenerated(t *testing.T) { if Choose(false)!=0 { t.Fatal(\"wrong choice\") } }\n"
	}
	if err = os.WriteFile(filepath.Join(created["path"], testPath), []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	revision, err := ledger.JobChangedPathsSHA256(created["path"], created["snapshot"])
	if err != nil {
		t.Fatal(err)
	}
	run := jobs.MaintenanceTestRun{JobID: fake.jobID, BaselineHead: created["head"], BaselineSnapshot: created["snapshot"], Target: 90,
		Before:     testcoverage.Evidence{Ecosystem: "go", Scope: "fixture", Flags: "offline", Status: "measured", Counts: testcoverage.Counts{Covered: 2, Executable: 4}},
		After:      testcoverage.Evidence{Ecosystem: "go", Scope: "fixture", Flags: "offline", Status: "measured", Counts: testcoverage.Counts{Covered: 3, Executable: 4}},
		AddedTests: []string{"fixture/TestGenerated"}, ExecutedTests: []string{"fixture/TestGenerated"}, NewTests: 1, Revision: revision}
	if target, ok := fake.started["coverage_target_pct"].(float64); ok {
		run.Target = target
	}
	run.BeforeOutput = filepath.Join(state, "before.jsonl")
	run.AfterOutput = filepath.Join(state, "after.jsonl")
	run.BeforeCoverage = filepath.Join(state, "before.coverprofile")
	run.AfterCoverage = filepath.Join(state, "after.coverprofile")
	for _, path := range []string{run.BeforeOutput, run.AfterOutput, run.BeforeCoverage, run.AfterCoverage} {
		if err = os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = writeJSONAtomic(filepath.Join(state, "codex_exec_mcp", "jobs", fake.jobID+".json"), map[string]any{"jobId": fake.jobID, "state": "SUCCEEDED", "repoRoot": root, "threadId": "fixture-thread", "acceptedAt": float64(time.Now().UnixNano()) / 1e9,
		"worktreePath": created["path"], "snapshot": created["snapshot"], "maintenance_test_job": true, "maintenance_identity_source": "host", "maintenance_job_id": fake.jobID, "maintenanceTests": run}); err != nil {
		t.Fatal(err)
	}
	project, key := contextio.ProjectKey(root), strings.Repeat("c", 64)
	if got := lease.New(filepath.Join(state, "projects", project)).Acquire(key, "maintenance-fixture", []string{testPath}, lease.AcquireOptions{}, float64(time.Now().UnixNano())/1e9); got.Status != "OK" {
		t.Fatal(got)
	}
	if err = writeJSONAtomic(filepath.Join(state, "async_jobs", fake.jobID+".json"), map[string]any{"jobId": fake.jobID, "project_key": project, "call_key": key}); err != nil {
		t.Fatal(err)
	}
}

func reviewMaintenancePatch(ctx context.Context, state, id string, _ map[string]any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	job, err := jobs.Read(filepath.Join(state, "codex_exec_mcp", "jobs", id+".json"))
	if err != nil {
		return err
	}
	var run jobs.MaintenanceTestRun
	job.Get("maintenanceTests", &run)
	if err = job.Set("reviewerOutcome", map[string]any{"execution_status": "COMPLETED", "verdict": "NO_MATERIAL_FINDING_IN_REVIEWED_SCOPE"}); err != nil {
		return err
	}
	if err = job.Set("reviewedSnapshot", run.BaselineSnapshot); err != nil {
		return err
	}
	if err = jobs.Write(filepath.Join(state, "codex_exec_mcp", "jobs", id+".json"), job); err != nil {
		return err
	}
	path := filepath.Join(state, "bundle_artifacts", id+".json")
	if err = writeJSONAtomic(path, map[string]any{"reviewer1_result": "NO_MATERIAL_FINDING_IN_REVIEWED_SCOPE", "validation": "PASS", "changed_paths_sha256": run.Revision, "thread_id": "fixture-thread", "routing_enforced": true, "task_contract_grounded": true}); err != nil {
		return err
	}
	index, err := jobs.Read(filepath.Join(state, "async_jobs", id+".json"))
	if err != nil {
		return err
	}
	if err = index.Set("stage", "CONSUMED"); err != nil {
		return err
	}
	if err = index.Set("bundle_path", path); err != nil {
		return err
	}
	return jobs.Write(filepath.Join(state, "async_jobs", id+".json"), index)
}

func TestMaintenanceFinalizationFailureDirections(t *testing.T) {
	for _, failure := range []string{"preemption", "deadline", "interrupted-publication", "expired-artifact", "skipped-review", "review-mutated-patch"} {
		t.Run(failure, func(t *testing.T) {
			root, state := maintenancePatchRepo(t), maintenanceScratch(t)
			fake := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}, cancel: map[string]any{"state": "CANCELLED"}}
			fake.onStart = func(state string) { stageMaintenancePatch(t, state, root, fake) }
			userJob := func() error {
				return writeJSONAtomic(filepath.Join(state, "codex_exec_mcp", "jobs", strings.Repeat("b", 64)+".json"), map[string]any{"state": "RUNNING", "repoRoot": root})
			}
			fake.finalize = func(ctx context.Context, state, id string, result map[string]any) error {
				if failure == "preemption" || failure == "deadline" {
					if failure == "preemption" {
						if err := userJob(); err != nil {
							return err
						}
					}
					<-ctx.Done()
					return ctx.Err()
				}
				if err := reviewMaintenancePatch(ctx, state, id, result); err != nil {
					return err
				}
				job, err := jobs.Read(filepath.Join(state, "codex_exec_mcp", "jobs", id+".json"))
				if err != nil {
					return err
				}
				if failure == "expired-artifact" {
					var run jobs.MaintenanceTestRun
					job.Get("maintenanceTests", &run)
					return os.Remove(run.BeforeOutput)
				}
				if failure == "skipped-review" {
					_ = job.Set("reviewerOutcome", map[string]any{"execution_status": "SKIPPED"})
					return jobs.Write(filepath.Join(state, "codex_exec_mcp", "jobs", id+".json"), job)
				}
				if failure == "review-mutated-patch" {
					var worktree string
					job.Get("worktreePath", &worktree)
					return os.WriteFile(filepath.Join(worktree, "source_test.go"), []byte("package fixture\nimport \"testing\"\nfunc TestGenerated(t *testing.T) { if value()!=2 { t.Fatal(\"changed assertion\") } }\n"), 0600)
				}
				return nil
			}
			oldRename := renameTestProposal
			if failure == "interrupted-publication" {
				renameTestProposal = func(src, dst string) error {
					if err := os.Rename(src, dst); err != nil {
						return err
					}
					return userJob()
				}
			}
			t.Cleanup(func() { renameTestProposal = oldRename })
			timeout := 5 * time.Second
			if failure == "deadline" {
				timeout = 1500 * time.Millisecond
			}
			runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "fixture", PollInterval: 10 * time.Millisecond, JobTimeout: timeout}
			if _, err := runner.Run(state, root, "test-gaps", "write tests"); err == nil {
				t.Fatal("failed finalization produced success")
			}
			if _, err := os.Stat(filepath.Join(MaintenanceDirectory(state, root), "reports", "test-gaps", "ready.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ready proposal remains: %v", err)
			}
			owner, err := lease.New(filepath.Join(state, "projects", contextio.ProjectKey(root))).OwnerState(strings.Repeat("c", 64), float64(time.Now().UnixNano())/1e9)
			if err != nil || owner != "RELEASED" {
				t.Fatalf("lease=%q err=%v", owner, err)
			}
		})
	}
}

func TestMaintenanceUnconfirmedFinalizerRetainsLeaseAndMarker(t *testing.T) {
	root, state := maintenancePatchRepo(t), maintenanceScratch(t)
	fake := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}, cancel: map[string]any{"state": "CANCELLED"}}
	fake.onStart = func(state string) { stageMaintenancePatch(t, state, root, fake) }
	release, stopped := make(chan struct{}), make(chan struct{})
	defer func() {
		close(release)
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("fixture finalizer did not stop")
		}
	}()
	fake.finalize = func(ctx context.Context, state, id string, result map[string]any) error {
		defer close(stopped)
		if err := writeJSONAtomic(filepath.Join(state, "codex_exec_mcp", "jobs", strings.Repeat("b", 64)+".json"), map[string]any{"state": "RUNNING", "repoRoot": root}); err != nil {
			return err
		}
		// Ignore cancellation until the test observes cleanup after the five-second grace period.
		<-release
		return ctx.Err()
	}
	runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "fixture", PollInterval: 10 * time.Millisecond, JobTimeout: 15 * time.Second}
	if _, err := runner.Run(state, root, "test-gaps", "write tests"); err == nil {
		t.Fatal("unconfirmed finalization produced success")
	}
	select {
	case <-stopped:
		t.Fatal("fixture finalizer stopped before cleanup assertions")
	default:
	}
	owner, err := lease.New(filepath.Join(state, "projects", contextio.ProjectKey(root))).OwnerState(strings.Repeat("c", 64), float64(time.Now().UnixNano())/1e9)
	if err != nil || owner != "ACTIVE" {
		t.Fatalf("lease=%q err=%v", owner, err)
	}
	for _, path := range []string{
		filepath.Join(state, "maintenance", "active.json"),
		filepath.Join(MaintenanceDirectory(state, root), "test-gaps-active.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var marker map[string]any
		if err := json.Unmarshal(data, &marker); err != nil {
			t.Fatal(err)
		}
		if marker["state"] != "TERMINATION_UNCONFIRMED" || marker["job_id"] != fake.jobID {
			t.Fatalf("retained marker %s = %#v", path, marker)
		}
	}
}

func TestMaintenanceAdmissionPreempted(t *testing.T) {
	root, state := maintenancePatchRepo(t), maintenanceScratch(t)
	fake := &fakeJobs{waitStart: true}
	fake.onStart = func(state string) {
		if err := writeJSONAtomic(filepath.Join(state, "codex_exec_mcp", "jobs", strings.Repeat("b", 64)+".json"), map[string]any{"state": "RUNNING", "repoRoot": root}); err != nil {
			t.Fatal(err)
		}
	}
	runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "fixture", PollInterval: 10 * time.Millisecond, JobTimeout: 5 * time.Second}
	started := time.Now()
	if _, err := runner.Run(state, root, "test-gaps", "write tests"); err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("admission was not promptly preempted: %v", err)
	}
}

func TestRunStartsWithMissingOrEmptyJobStore(t *testing.T) {
	for _, emptyDir := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "empty"}[emptyDir], func(t *testing.T) {
			state := t.TempDir()
			if emptyDir {
				if err := os.MkdirAll(filepath.Join(state, "codex_exec_mcp", "jobs"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			fake := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}}
			runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "gpt-6-luna", Sleep: func(time.Duration) {}}
			if _, err := runner.Run(state, t.TempDir(), "cleanup", "prompt"); err != nil {
				t.Fatalf("maintenance did not start: %v", err)
			}
			if fake.started == nil {
				t.Fatal("maintenance job was not started")
			}
		})
	}
}

func TestRunDeadlineCancelsActiveJob(t *testing.T) {
	state, repo := t.TempDir(), t.TempDir()
	fake := &fakeJobs{results: []map[string]any{{"state": "RUNNING"}}, cancel: map[string]any{"state": "CANCELLED"}}
	current := time.Unix(100, 0)
	runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "gpt-6-luna", Now: func() time.Time { return current },
		Sleep: func(d time.Duration) { current = current.Add(d) }, PollInterval: time.Second, JobTimeout: 2 * time.Second}
	if _, err := runner.Run(state, repo, "cleanup", "prompt"); err == nil || err.Error() != "maintenance deadline expired" {
		t.Fatalf("deadline error = %v", err)
	}
	if fake.cancelCalls != 1 {
		t.Fatalf("deadline cancellation calls = %d", fake.cancelCalls)
	}
}

func TestRunSkipsUnattributableCorruptJobFile(t *testing.T) {
	state := t.TempDir()
	jobsDir := filepath.Join(state, "codex_exec_mcp", "jobs")
	if err := os.MkdirAll(jobsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobsDir, "broken.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}}
	runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "gpt-6-luna", Sleep: func(time.Duration) {}}
	if _, err := runner.Run(state, t.TempDir(), "cleanup", "prompt"); err != nil {
		t.Fatalf("unattributable corrupt job file blocked maintenance: %v", err)
	}
	if fake.started == nil {
		t.Fatal("maintenance job was not started")
	}
}

func TestRunSkipsProjectJobWithoutState(t *testing.T) {
	state, root := t.TempDir(), t.TempDir()
	jobsDir := filepath.Join(state, "codex_exec_mcp", "jobs")
	if err := os.MkdirAll(jobsDir, 0700); err != nil {
		t.Fatal(err)
	}
	broken, _ := json.Marshal(map[string]any{"repoRoot": root})
	if err := os.WriteFile(filepath.Join(jobsDir, "broken.json"), broken, 0600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}}
	runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "gpt-6-luna", Sleep: func(time.Duration) {}}
	if _, err := runner.Run(state, root, "cleanup", "prompt"); err == nil || err.Error() != "active or unknown user job state; maintenance skipped" {
		t.Fatalf("project job without state error = %v", err)
	}
	if fake.started != nil {
		t.Fatal("maintenance job started despite unknown state for this project's job")
	}
}

func TestRunnerUsesProjectCoderConfiguration(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	if err := os.MkdirAll(repository, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USERPROFILE", filepath.Join(root, "user"))
	t.Setenv("PURSER_STATE_DIR", filepath.Join(root, "state"))
	rolePath := profile.ProjectRolesPath(filepath.Join(root, "state"), repository)
	if err := os.MkdirAll(filepath.Dir(rolePath), 0700); err != nil {
		t.Fatal(err)
	}
	roles := []byte("schema_version = 1\n[roles]\ncoder = \"opencode\"\n[coding]\ncoder_agent = \"opencode\"\n[coding.routes]\nmechanical = \"deepseek/deepseek-v4-pro\"\nsubtle = \"deepseek/deepseek-v4-pro\"\nnormal = \"deepseek/deepseek-v4-pro\"\nconsequential = \"deepseek/deepseek-v4-pro\"\nescalated = \"deepseek/deepseek-v4-pro\"\noperation = \"deepseek/deepseek-v4-pro\"\nplan_standard = \"deepseek/deepseek-v4-pro\"\n")
	if err := os.WriteFile(rolePath, roles, 0600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeJobs{results: []map[string]any{{"state": "SUCCEEDED"}}}
	runner := Runner{Jobs: fake, Sleep: func(time.Duration) {}}
	if _, err := runner.Run(filepath.Join(root, "state"), repository, "cleanup", "prompt"); err != nil {
		t.Fatal(err)
	}
	if fake.started["agent"] != "opencode" || fake.started["jobModel"] != "deepseek/deepseek-v4-pro" {
		t.Fatalf("maintenance start args ignored project coder: %#v", fake.started)
	}
}

func fileExists(path string) bool { _, err := os.Stat(path); return !errors.Is(err, os.ErrNotExist) }
