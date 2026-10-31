package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"purser-hook/internal/fsretry"
	"purser-hook/internal/jobs"
	"purser-hook/internal/lease"
	"purser-hook/internal/ledger"
	"purser-hook/internal/proc"
	"purser-hook/internal/review"
	"purser-hook/internal/testcoverage"
)

type testPatchFinalizer interface {
	Finalize(context.Context, string, string, map[string]any) error
}

var ErrTestPatchTerminationUnconfirmed = errors.New("maintenance finalizer termination unconfirmed")

// ReserveTestJob binds a private host reservation to the real enforced call.
func ReserveTestJob(state, key string, run jobs.MaintenanceTestRun) error {
	path := filepath.Join(state, "pending", key+".json")
	pending, err := jobs.Read(path)
	if err != nil {
		return err
	}
	var call string
	pending.Get("async_call", &call)
	sum := sha256.Sum256([]byte(call))
	if call == "" || hex.EncodeToString(sum[:]) != run.JobID {
		return errors.New("maintenance reservation identity mismatch")
	}
	for field, value := range map[string]any{"maintenance_test_job": true, "maintenance_identity_source": "host", "maintenance_job_id": run.JobID} {
		if err = pending.Set(field, value); err != nil {
			return err
		}
	}
	if err = jobs.Write(path, pending); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(state, "maintenance", "jobs", run.JobID+".json"), run)
}

func testPatchEvidence(state, root, id string, baseline maintenanceBaseline, reviewed bool) (map[string]any, error) {
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return nil, errors.New("invalid maintenance job identity")
	}
	job, err := jobs.Read(filepath.Join(state, "codex_exec_mcp", "jobs", id+".json"))
	if err != nil {
		return nil, err
	}
	var worktree, snapshot, source, recordedID, status, thread, actualID, repository string
	var maintenance bool
	var run jobs.MaintenanceTestRun
	job.Get("state", &status)
	job.Get("jobId", &actualID)
	job.Get("repoRoot", &repository)
	job.Get("worktreePath", &worktree)
	job.Get("snapshot", &snapshot)
	job.Get("threadId", &thread)
	job.Get("maintenance_test_job", &maintenance)
	job.Get("maintenance_identity_source", &source)
	job.Get("maintenance_job_id", &recordedID)
	if !job.Get("maintenanceTests", &run) || actualID != id || !sameProjectPath(repository, root) || status != "SUCCEEDED" || !maintenance || run.JobID != id || run.BaselineHead != baseline.head || run.BaselineSnapshot != baseline.snapshot || snapshot != run.BaselineSnapshot || sameProjectPath(worktree, root) {
		return nil, errors.New("maintenance host evidence or isolated baseline is unavailable")
	}
	if !testcoverage.Compare(run.Before, run.After) || run.NewTests < 1 || run.NewTests != jobs.CountExecutedMaintenanceTests(run.AddedTests, run.ExecutedTests) {
		return nil, errors.New("maintenance patch has no measured improvement or executed new tests")
	}
	delta := ledger.ComputeJobDelta(worktree, snapshot)
	if delta["current_head"] != run.BaselineHead {
		return nil, errors.New("maintenance worktree HEAD moved")
	}
	if err = jobs.ValidateMaintenanceTestDelta(worktree, delta, jobs.MaintenanceTestIdentity{JobID: id, RecordedID: recordedID, RecordedBy: source}); err != nil {
		return nil, err
	}
	revision, err := ledger.JobChangedPathsSHA256(worktree, snapshot)
	if err != nil || revision != run.Revision {
		return nil, errors.New("maintenance patch revision changed")
	}
	for _, path := range []string{run.BeforeOutput, run.AfterOutput, run.BeforeCoverage, run.AfterCoverage} {
		if info, e := os.Stat(path); e != nil || !info.Mode().IsRegular() {
			return nil, errors.New("maintenance host test artifact missing")
		}
	}
	var accepted float64
	job.Get("acceptedAt", &accepted)
	expires := time.Unix(0, int64(accepted*1e9)).Add(72 * time.Hour)
	if !time.Now().Before(expires) {
		return nil, errors.New("maintenance job artifacts expired")
	}
	proposal := map[string]any{"job_id": id, "thread_id": thread, "project_root": root, "baseline_head": baseline.head, "baseline_snapshot": baseline.snapshot,
		"changed_paths_sha256": revision, "changed_test_paths": delta["changed_paths"], "before": run.Before, "after": run.After, "new_test_count": run.NewTests,
		"new_test_identities": run.AddedTests, "coverage_target_pct": run.Target, "expires_at": expires.UTC().Format(time.RFC3339Nano)}
	proposal["coverage_artifacts"] = []string{run.BeforeCoverage, run.AfterCoverage}
	proposal["test_artifacts"] = []string{run.BeforeOutput, run.AfterOutput}
	if !reviewed {
		return proposal, nil
	}
	index, err := jobs.Read(filepath.Join(state, "async_jobs", id+".json"))
	if err != nil {
		return nil, err
	}
	var stage, path string
	index.Get("stage", &stage)
	index.Get("bundle_path", &path)
	if stage != "CONSUMED" || filepath.Dir(path) != filepath.Join(state, "bundle_artifacts") {
		return nil, errors.New("maintenance review artifact is unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bundle map[string]any
	if json.Unmarshal(data, &bundle) != nil {
		return nil, errors.New("invalid maintenance review artifact")
	}
	var facts map[string]any
	job.Get("reviewerOutcome", &facts)
	var reviewedSnapshot string
	job.Get("reviewedSnapshot", &reviewedSnapshot)
	outcome := review.Outcome{ReviewStatus: stringValue(facts["execution_status"], ""), Result: stringValue(bundle["reviewer1_result"], ""), Validation: stringValue(bundle["validation"], "")}
	if !review.Passed(outcome, stringValue(bundle["reviewer1_skipped_reason"], "")) || facts["verdict"] != outcome.Result || reviewedSnapshot != snapshot || bundle["routing_enforced"] != true || bundle["task_contract_grounded"] != true || bundle["changed_paths_sha256"] != revision || bundle["thread_id"] != thread || bundle["force_full_review"] == true || bundle["reviewer1_scope"] != nil {
		findings, _ := json.Marshal(bundle["findings"])
		return nil, fmt.Errorf("maintenance review not clean: %s", findings)
	}
	sum := sha256.Sum256(data)
	proposal["review_artifact"], proposal["review_artifact_sha256"] = path, hex.EncodeToString(sum[:])
	return proposal, nil
}

var renameTestProposal = os.Rename

func publishTestProposal(ctx context.Context, state, root, path string, baseline maintenanceBaseline, proposal map[string]any) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, removeTestProposal(path))
		}
	}()
	data, err := json.Marshal(proposal)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".ready-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	check := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if user, known := activeUserJobs(state, root, stringValue(proposal["job_id"], "")); !known || user {
			return errors.New("maintenance yielded to user work before publication")
		}
		if err := verifyMaintenanceBaseline(root, baseline); err != nil {
			return err
		}
		current, err := testPatchEvidence(state, root, stringValue(proposal["job_id"], ""), baseline, true)
		if err != nil {
			return err
		}
		if current["changed_paths_sha256"] != proposal["changed_paths_sha256"] || current["review_artifact_sha256"] != proposal["review_artifact_sha256"] {
			return errors.New("maintenance evidence changed during publication")
		}
		return ctx.Err()
	}
	if err = check(); err != nil {
		return err
	}
	err = fsretry.ReplaceWith(func(src, dst string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return renameTestProposal(src, dst)
	}, file.Name(), path)
	if err != nil {
		return err
	}
	return check()
}

func removeTestProposal(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tombstone, err := os.CreateTemp(filepath.Dir(path), ".ready-removed-*")
	if err != nil {
		return err
	}
	tombstonePath := tombstone.Name()
	if err = tombstone.Close(); err != nil {
		_ = os.Remove(tombstonePath)
		return err
	}
	if err = os.Remove(tombstonePath); err != nil {
		return err
	}
	err = fsretry.ReplaceWith(os.Rename, path, tombstonePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	err = os.Remove(tombstonePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r Runner) finishTestPatch(ctx context.Context, state, root, id, readyPath string, baseline maintenanceBaseline, result map[string]any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := verifyMaintenanceBaseline(root, baseline); err != nil {
		return err
	}
	if _, err := testPatchEvidence(state, root, id, baseline, false); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	finalizer, ok := r.Jobs.(testPatchFinalizer)
	if !ok {
		return errors.New("maintenance enforcement finalizer unavailable")
	}
	if err := finalizer.Finalize(ctx, state, id, result); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	proposal, err := testPatchEvidence(state, root, id, baseline, true)
	if err != nil {
		return err
	}
	if !lease.ReleaseJobScopeLease(state, id) {
		return errors.New("maintenance scope lease release failed")
	}
	proposal["status"] = "ready"
	return publishTestProposal(ctx, state, root, readyPath, baseline, proposal)
}

type maintenanceBaseline struct {
	head     string
	snapshot string
}

func captureMaintenanceBaseline(root string) (maintenanceBaseline, error) {
	status, err := maintenanceGit(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return maintenanceBaseline{}, err
	}
	if len(status) != 0 {
		return maintenanceBaseline{}, errors.New("test-gaps maintenance requires a clean checkout")
	}
	head, err := maintenanceGit(root, "rev-parse", "--verify", "HEAD")
	if err != nil || len(strings.TrimSpace(head)) < 40 {
		return maintenanceBaseline{}, errors.New("test-gaps maintenance baseline HEAD is unavailable")
	}
	snapshot, err := ledger.CheckoutSnapshot(root)
	if err != nil {
		return maintenanceBaseline{}, err
	}
	tree, err := maintenanceGit(root, "rev-parse", "HEAD^{tree}")
	if err != nil || strings.TrimSpace(tree) != snapshot {
		return maintenanceBaseline{}, errors.New("test-gaps maintenance requires a clean checkout")
	}
	current, err := maintenanceGit(root, "rev-parse", "HEAD")
	if err != nil || current != head {
		return maintenanceBaseline{}, errors.New("test-gaps checkout baseline changed during capture")
	}
	return maintenanceBaseline{head: strings.TrimSpace(head), snapshot: snapshot}, nil
}

func verifyMaintenanceBaseline(root string, baseline maintenanceBaseline) error {
	current, err := captureMaintenanceBaseline(root)
	if err != nil {
		return err
	}
	if current != baseline {
		return errors.New("test-gaps checkout baseline changed during maintenance")
	}
	return nil
}

func maintenanceGit(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := proc.Hide(exec.CommandContext(ctx, "git", append([]string{"-C", root, "-c", "core.longpaths=true"}, args...)...))
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	proc.ConfigureProcessTreeKill(command)
	output, err := command.Output()
	if err != nil {
		return "", errors.New("test-gaps checkout baseline could not be verified")
	}
	return string(output), nil
}
