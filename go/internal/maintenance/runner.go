package maintenance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/fsretry"
	"purser-hook/internal/jobs"
	"purser-hook/internal/lease"
	"purser-hook/internal/models"
	"purser-hook/internal/obsidian"
	"purser-hook/internal/policy"
	"purser-hook/internal/profile"
)

const (
	JobTimeout   = 45 * time.Minute
	PollInterval = time.Second
)

// JobOperations is the small boundary around the Go MCP host's jobs operations.
// Implementations call the existing jobs start/result/cancel operations.
type JobOperations interface {
	Start(context.Context, string, map[string]any) (map[string]any, error)
	Result(state, jobID string) map[string]any
	Cancel(state, jobID string) map[string]any
}

type Runner struct {
	Jobs         JobOperations
	CoderAgent   string
	CoderModel   string
	CoderEffort  string
	Now          func() time.Time
	Sleep        func(time.Duration)
	PollInterval time.Duration
	JobTimeout   time.Duration
}

// Run starts a maintenance job and returns its raw terminal result. Test-gap
// jobs write only in a private implementation worktree; report parsing and
// persistence belong to the caller.
func (r Runner) Run(state, repository, reportType, prompt string) (map[string]any, error) {
	if r.Jobs == nil {
		return nil, errors.New("maintenance job operations are required")
	}
	if reportType != "cleanup" && reportType != "test-gaps" {
		return nil, fmt.Errorf("unsupported maintenance report type: %s", reportType)
	}
	now, sleep, poll, timeout := r.clock()
	deadline := now().Add(timeout)
	if err := r.resolveCoder(repository); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(repository)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if relative, e := filepath.Rel(root, state); e == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("maintenance state must be outside the user checkout")
	}
	var baseline maintenanceBaseline
	readyPath := filepath.Join(MaintenanceDirectory(state, root), "reports", "test-gaps", "ready.json")
	if reportType == "test-gaps" {
		baseline, err = captureMaintenanceBaseline(root)
		if err != nil {
			return nil, errors.Join(err, removeTestProposal(readyPath))
		}
	}
	maintenanceDir := filepath.Join(state, "projects", contextio.ProjectKey(root), "maintenance")
	activePath := filepath.Join(maintenanceDir, reportType+"-active.json")
	if err := os.MkdirAll(maintenanceDir, 0700); err != nil {
		return nil, err
	}
	if !now().Before(deadline) {
		return nil, errors.New("maintenance deadline expired before job start")
	}
	releaseSlot, err := contextio.AcquireMaintenanceSlot(state)
	if err != nil {
		return nil, err
	}
	retainSlot := false
	retainedJob := ""
	defer func() {
		if !retainSlot {
			releaseSlot()
		} else {
			_ = replaceActive(filepath.Join(state, "maintenance", "active.json"), map[string]any{"state": "TERMINATION_UNCONFIRMED", "job_id": retainedJob, "project_root": root})
		}
	}()
	startedAt := now().UTC().Format("20060102T150405.000000Z")
	active := map[string]any{"project_root": root, "started_at": startedAt, "state": "RUNNING"}
	if err := contextio.CreateOrRecoverMaintenanceActive(activePath, state, active, now()); err != nil {
		return nil, err
	}
	defer func() {
		if retainSlot {
			active["state"], active["job_id"] = "TERMINATION_UNCONFIRMED", retainedJob
			_ = replaceActive(activePath, active)
		} else {
			_ = contextio.ClearMaintenanceActive(activePath)
		}
	}()
	if userJobs, known := activeUserJobs(state, root, ""); !known || userJobs {
		if reportType == "test-gaps" {
			_ = removeTestProposal(readyPath)
		}
		return nil, errors.New("active or unknown user job state; maintenance skipped")
	}
	if reportType == "test-gaps" {
		if data, e := os.ReadFile(readyPath); e == nil {
			var prior map[string]any
			if json.Unmarshal(data, &prior) == nil {
				id := stringValue(prior["job_id"], "")
				expires, e := time.Parse(time.RFC3339Nano, stringValue(prior["expires_at"], ""))
				if current, checkErr := testPatchEvidence(state, root, id, baseline, true); checkErr == nil && e == nil && now().Before(expires) && prior["status"] == "ready" && prior["changed_paths_sha256"] == current["changed_paths_sha256"] && prior["review_artifact_sha256"] == current["review_artifact_sha256"] {
					return map[string]any{"jobId": id, "state": "SUCCEEDED", "proposal_path": readyPath}, nil
				}
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
		if err := removeTestProposal(readyPath); err != nil {
			return nil, err
		}
	}
	call, err := randomID()
	if err != nil {
		return nil, err
	}
	remaining := deadline.Sub(now())
	if remaining <= 0 {
		return nil, errors.New("maintenance deadline expired before job start")
	}
	startArgs := map[string]any{
		"call": call, "mode": "new", "prompt": prompt, "cwd": root,
		"sandbox": "read-only", "jobPhase": "operation", "agent": r.CoderAgent,
		"jobModel": r.CoderModel, "jobEffort": r.CoderEffort,
		"timeoutSeconds": remaining.Seconds(),
	}
	if reportType == "test-gaps" {
		startArgs["sandbox"], startArgs["jobPhase"], startArgs["worktree"] = "workspace-write", "implementation", true
		target := profile.LoadProfile(root).CoverageTargetPct
		if target <= 0 {
			target = 90
		}
		startArgs["coverage_target_pct"], startArgs["maintenanceBaselineHead"], startArgs["maintenanceBaselineSnapshot"], startArgs["maintenanceDeadlineAt"] = target, baseline.head, baseline.snapshot, deadline.UTC().Format(time.RFC3339Nano)
		startArgs["prompt"] = fmt.Sprintf("%s\nResolved coverage_target_pct: %g%%. Host measurements alone determine improvement and readiness. Do not commit, push, or apply changes.", prompt, target)
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), deadline.Sub(now()))
	defer startCancel()
	starting := make(chan struct{})
	admissionStopped := make(chan error, 1)
	if reportType == "test-gaps" {
		go func() {
			ticker := time.NewTicker(poll)
			defer ticker.Stop()
			for {
				select {
				case <-starting:
					return
				case <-startCtx.Done():
					admissionStopped <- startCtx.Err()
					return
				case <-ticker.C:
					if user, known := activeUserJobs(state, root, ""); !known || user {
						startCancel()
						admissionStopped <- fmt.Errorf("maintenance yielded to a user %s job during admission", coderLabel(r.CoderAgent))
						return
					}
				}
			}
		}()
	}
	started, err := r.Jobs.Start(startCtx, state, startArgs)
	close(starting)
	select {
	case stopped := <-admissionStopped:
		err = errors.Join(err, stopped)
	default:
	}
	if err != nil {
		if errors.Is(err, ErrTestPatchTerminationUnconfirmed) {
			retainSlot = true
		}
		if id := stringValue(structured(started)["jobId"], ""); id != "" {
			retainedJob = id
			retainSlot = retainSlot || !terminal(structured(r.Jobs.Cancel(state, id))["state"], false)
			if !retainSlot {
				_ = lease.ReleaseJobScopeLease(state, id)
			}
		}
		return nil, err
	}
	started = structured(started)
	jobID, ok := started["jobId"].(string)
	if !ok || jobID == "" {
		return nil, errors.New(stringValue(started["message"], "durable "+coderLabel(r.CoderAgent)+" job could not be started"))
	}
	if reportType == "test-gaps" {
		defer func() {
			if !retainSlot {
				_ = lease.ReleaseJobScopeLease(state, jobID)
			}
		}()
	}
	retainedJob = jobID
	active["job_id"] = jobID
	if err := replaceActive(activePath, active); err != nil {
		retainSlot = !terminal(structured(r.Jobs.Cancel(state, jobID))["state"], false)
		return nil, err
	}
	for {
		if !now().Before(deadline) {
			cancelled := terminal(structured(r.Jobs.Cancel(state, jobID))["state"], false)
			if !cancelled {
				retainSlot = true
				return nil, errors.New("maintenance deadline expired; cancellation was unconfirmed and global slot retained")
			}
			return nil, errors.New("maintenance deadline expired")
		}
		if userJobs, known := activeUserJobs(state, root, jobID); !known || userJobs {
			cancelled := terminal(structured(r.Jobs.Cancel(state, jobID))["state"], false)
			if !cancelled {
				for now().Before(deadline) {
					result := structured(r.Jobs.Result(state, jobID))
					if terminal(result["state"], false) {
						return nil, errors.New("maintenance yielded or stopped because user job state became unknown")
					}
					sleep(minDuration(poll, deadline.Sub(now())))
				}
				retainSlot = true
				return nil, errors.New("maintenance cancellation was unconfirmed; global slot retained by job timeout")
			}
			return nil, fmt.Errorf("maintenance yielded to a user %s job", coderLabel(r.CoderAgent))
		}
		result := structured(r.Jobs.Result(state, jobID))
		status := stringValue(result["state"], "")
		if status == "SUCCEEDED" {
			if result["jobId"] == nil {
				result["jobId"] = jobID
			}
			if reportType == "test-gaps" {
				ctx, cancel := context.WithTimeout(context.Background(), deadline.Sub(now()))
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- r.finishTestPatch(ctx, state, root, jobID, readyPath, baseline, result) }()
				for {
					select {
					case err := <-done:
						if errors.Is(err, ErrTestPatchTerminationUnconfirmed) {
							retainSlot = true
						}
						if err != nil {
							_ = removeTestProposal(readyPath)
							return result, err
						}
						result["proposal_path"] = readyPath
						return result, nil
					case <-time.After(minDuration(poll, deadline.Sub(now()))):
						user, known := activeUserJobs(state, root, jobID)
						if !now().Before(deadline) || !known || user {
							cancel()
							select {
							case err := <-done:
								retainSlot = errors.Is(err, ErrTestPatchTerminationUnconfirmed)
							case <-time.After(5 * time.Second):
								retainSlot = true
							}
							if !retainSlot {
								_ = r.Jobs.Cancel(state, jobID)
							}
							return nil, errors.Join(errors.New("maintenance finalization preempted or deadline expired"), removeTestProposal(readyPath))
						}
					}
				}
			}
			return result, nil
		}
		if terminal(status, true) {
			if status == "UNKNOWN_OR_IN_PROGRESS" {
				retainSlot = true
			}
			if result["jobId"] == nil {
				result["jobId"] = jobID
			}
			if reportType == "test-gaps" && status == "FAILED" && result["errorCode"] == "COVERAGE_UNKNOWN" {
				return result, fmt.Errorf("%w: %s", jobs.ErrCoverageUnknown, stringValue(result["message"], ""))
			}
			return result, fmt.Errorf("%s job %s: %s %s", coderLabel(r.CoderAgent), status, stringValue(result["errorCode"], ""), stringValue(result["message"], ""))
		}
		sleep(minDuration(poll, deadline.Sub(now())))
	}
}

func coderLabel(agent string) string {
	switch agent {
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "agy":
		return "Antigravity"
	case "claude":
		return "Claude"
	default:
		return agent
	}
}

func (r *Runner) resolveCoder(repository string) error {
	if r.CoderAgent != "" && r.CoderModel != "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	config, err := policy.LoadCoderConfigForRepository(obsidian.ConfigPath(), filepath.Join(home, ".orchestrator", "config.toml"), repository)
	if err != nil {
		return fmt.Errorf("maintenance coder configuration: %w", err)
	}
	agent, _ := config["coder_agent"].(string)
	routes, _ := config["routes"].(map[string]any)
	route, _ := routes["operation"].(map[string]any)
	model, _ := route["model"].(string)
	effort, _ := route["effort"].(string)
	if agent == "" || model == "" {
		return errors.New("maintenance operation coder route is unavailable")
	}
	r.CoderAgent, r.CoderModel, r.CoderEffort = agent, models.ResolveLatest(agent, model), effort
	return nil
}

func (r Runner) clock() (func() time.Time, func(time.Duration), time.Duration, time.Duration) {
	now, sleep, poll, timeout := r.Now, r.Sleep, r.PollInterval, r.JobTimeout
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	if poll <= 0 {
		poll = PollInterval
	}
	if timeout <= 0 {
		timeout = JobTimeout
	}
	return now, sleep, poll, timeout
}

func activeUserJobs(state, root, exclude string) (bool, bool) {
	dir := filepath.Join(state, "codex_exec_mcp", "jobs")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			// Job files may be briefly locked by antivirus; without a readable
			// project field this file cannot make this project's state unknown.
			continue
		}
		var job struct {
			State    any    `json:"state"`
			JobID    any    `json:"jobId"`
			CWD      string `json:"cwd"`
			RepoRoot string `json:"repoRoot"`
		}
		decodeErr := json.Unmarshal(data, &job)
		project := job.RepoRoot
		if project == "" {
			project = job.CWD
		}
		if project == "" || !sameProjectPath(project, root) {
			continue
		}
		if decodeErr != nil {
			return false, false
		}
		status, ok := job.State.(string)
		if !ok {
			return false, false
		}
		idValue, exists := job.JobID, job.JobID != nil
		id, ok := idValue.(string)
		if !exists {
			id = strings.TrimSuffix(entry.Name(), ".json")
		} else if !ok {
			id = ""
		}
		if id != exclude && (status == "RUNNING" || status == "QUEUED") {
			// This host-owned reservation precedes launch and makes admission observable.
			if marker, e := os.ReadFile(filepath.Join(state, "maintenance", "jobs", id+".json")); e == nil {
				var run jobs.MaintenanceTestRun
				if json.Unmarshal(marker, &run) == nil && run.JobID == id && run.BaselineHead != "" {
					continue
				}
			}
			record, err := jobs.Decode(data)
			if err != nil || !jobs.StaleHost(dir, record, time.Now()) {
				return true, true
			}
		}
	}
	return false, true
}

func sameProjectPath(a, b string) bool {
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

func replaceActive(path string, active map[string]any) error {
	data, err := json.Marshal(active)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".maintenance-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return fsretry.Replace(name, path)
}

func randomID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(id[0:4]), hex.EncodeToString(id[4:6]), hex.EncodeToString(id[6:8]), hex.EncodeToString(id[8:10]), hex.EncodeToString(id[10:])), nil
}

func structured(value map[string]any) map[string]any {
	if nested, ok := value["structuredContent"].(map[string]any); ok {
		return nested
	}
	return value
}

func terminal(value any, includeUnknown bool) bool {
	switch value {
	case "SUCCEEDED", "FAILED", "CANCELLED", "TIMED_OUT":
		return true
	case "UNKNOWN_OR_IN_PROGRESS":
		return includeUnknown
	default:
		return false
	}
}

func stringValue(value any, fallback string) string {
	if s, ok := value.(string); ok {
		return s
	}
	return fallback
}
func minDuration(a, b time.Duration) time.Duration {
	if b < a {
		return b
	}
	return a
}
