package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/ledger"
	"purser-hook/internal/proc"
	"purser-hook/internal/profile"
	"purser-hook/internal/retention"
	"purser-hook/internal/testcoverage"
)

const hostUnavailableCode = "GO_HOST_EXECUTION_UNAVAILABLE"

func validJobID(value string) bool {
	for i := range value {
		c := value[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// HostOptions injects the child executable for parity tests and embedders.
type HostOptions struct {
	WorkerCommand func(LaunchArgs) ([]string, error)
	HostContained bool
	StateRoot     string
}

// worktreeRootNote tells a worktree worker to map absolute checkout paths into its worktree.
const worktreeRootNote = "\nUse the job worktree as project root; map absolute checkout paths to the same relative path here."

// RunHostGeneration owns and executes one queued agent generation.
func RunHostGeneration(dir, jobID string, generation int64, token string, options ...HostOptions) (map[string]any, error) {
	if len(jobID) != 64 || !validJobID(jobID) || generation < 1 || token == "" {
		return nil, errors.New("invalid host generation identity")
	}
	var opt HostOptions
	if len(options) > 0 {
		opt = options[0]
	}
	path := filepath.Join(dir, jobID+".json")
	job, err := Read(path)
	if err != nil {
		return nil, err
	}
	var worktreePath, state string
	var leaseQueued bool
	var recordedID, recordedToken string
	var recordedGeneration int64
	job.Get("jobId", &recordedID)
	job.Get("generation", &recordedGeneration)
	job.Get("ownershipToken", &recordedToken)
	job.Get("worktreePath", &worktreePath)
	job.Get("leaseQueued", &leaseQueued)
	job.Get("state", &state)
	if recordedID != jobID || recordedGeneration != generation || recordedToken != token || state != "QUEUED" {
		return map[string]any{"status": "stale"}, nil
	}
	launchPath := filepath.Join(dir, jobID+".launch")
	launchData, err := os.ReadFile(launchPath)
	if err != nil {
		return nil, err
	}
	var launch struct {
		Name     string          `json:"name"`
		Args     json.RawMessage `json:"args"`
		CodexBin any             `json:"codexBin"`
		Agent    string          `json:"agent"`
	}
	if err = json.Unmarshal(launchData, &launch); err != nil {
		return nil, err
	}
	var argsMap map[string]any
	if err = json.Unmarshal(launch.Args, &argsMap); err != nil || argsMap == nil {
		return nil, errors.New("launch args must be an object")
	}
	var args LaunchArgs
	if err = json.Unmarshal(launch.Args, &args); err != nil {
		return nil, err
	}
	var prompt string
	if value, ok := argsMap["prompt"].(string); ok {
		prompt = value
	}
	args.Name = launch.Name
	if args.ThreadID == "" {
		args.ThreadID, _ = argsMap["threadId"].(string)
	}
	args.OutputPath = filepath.Join(dir, jobID+".host-output")
	if args.Cwd == "" {
		job.Get("repoRoot", &args.Cwd)
		if args.Cwd == "" {
			args.Cwd, err = resolveRepositoryRoot()
			if err != nil {
				return finishHostError(path, jobID, generation, token, "WORKTREE_SNAPSHOT_FAILED", "Repository root could not be resolved for Codex launch")
			}
		}
	}
	repoRoot := ""
	if worktreePath == "" && argsMap["worktree"] != true {
		if root, rootErr := ledger.GitTopLevel(args.Cwd); rootErr == nil {
			repoRoot = root
		}
	}
	jobTempDir := ""
	workerAlive := false
	defer func() {
		if !workerAlive {
			_ = os.Remove(args.OutputPath)
		}
	}()
	defer func() {
		if jobTempDir == "" || workerAlive {
			return
		}
		finished, readErr := Read(path)
		var finalState string
		if readErr == nil && finished.Get("state", &finalState) && terminalJobs[finalState] {
			_ = os.RemoveAll(jobTempDir)
		}
	}()
	agent := launch.Agent
	if agent == "" {
		agent = "codex"
	}
	var route, phase string
	job.Get("route", &route)
	job.Get("phase", &phase)
	research := route == "research" && phase == "operation" && agent == "codex"
	if research && repoRoot == "" {
		job.Get("repoRoot", &repoRoot)
	}
	if launch.Name != "codex" && launch.Name != "codex-reply" {
		return finishHostError(path, jobID, generation, token, "CODEX_FAILED", "Invalid host launch tool")
	}
	deadline, ok := numericDeadline(job)
	if !ok {
		return nil, errors.New("job deadline is invalid")
	}
	terminal := make(chan struct{})
	var cancelled atomic.Bool
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, int64(deadline*1e9)))
	defer cancel()
	signalToken := ""
	job.Get("signalToken", &signalToken)
	if signalToken == "" {
		return nil, errors.New("job signal token is unavailable")
	}
	signal, err := StartSignalHost(jobID, signalToken, terminal, func() bool {
		if cancelled.Swap(true) {
			return false
		}
		cancel()
		current, readErr := Read(path)
		if readErr == nil {
			var currentState string
			current.Get("state", &currentState)
			if currentState == "QUEUED" {
				_ = finishJob(dir, jobID, "CANCELLED", "CANCELLED", nil, "Codex cancelled", float64(time.Now().UnixNano())/1e9, hostFinishOptions(generation, token))
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = signal.Close() }()
	createdValue, createdOK := hostProcessIdentity()
	if !createdOK {
		return finishHostError(path, jobID, generation, token, "PROCESS_UNCONFIRMED", "Host process identity unavailable")
	}
	{
		lock, lockErr := AcquireOwnership(dir, jobID)
		if lockErr != nil {
			return nil, lockErr
		}
		job, lockErr = Read(path)
		if lockErr == nil {
			job.Get("jobId", &recordedID)
			job.Get("generation", &recordedGeneration)
			job.Get("ownershipToken", &recordedToken)
			job.Get("state", &state)
			if recordedID != jobID || recordedGeneration != generation || recordedToken != token || state != "QUEUED" {
				_ = lock.Release()
				return map[string]any{"status": "stale"}, nil
			}
			address, _ := signal.Addr().(*net.TCPAddr)
			for _, field := range []struct {
				key   string
				value any
			}{
				{"hostPid", os.Getpid()}, {"hostCreatedAt", createdValue},
				{"hostContained", opt.HostContained}, {"signalPort", address.Port},
			} {
				if lockErr = job.Set(field.key, field.value); lockErr != nil {
					break
				}
			}
			if lockErr == nil && repoRoot != "" {
				lockErr = job.Set("repoRoot", repoRoot)
			}
			if lockErr == nil {
				lockErr = Write(path, job)
			}
		}
		if releaseErr := lock.Release(); lockErr == nil {
			lockErr = releaseErr
		}
		if lockErr != nil {
			return nil, lockErr
		}
	}
	var childEnv []string
	stateRoot := opt.StateRoot
	if stateRoot == "" {
		stateRoot = filepath.Dir(filepath.Dir(dir))
	}
	var maintenanceRun *MaintenanceTestRun
	marker, markerErr := os.ReadFile(filepath.Join(stateRoot, "maintenance", "jobs", jobID+".json"))
	if markerErr == nil {
		maintenanceRun = &MaintenanceTestRun{}
		if json.Unmarshal(marker, maintenanceRun) != nil || maintenanceRun.JobID != jobID {
			return finishHostError(path, jobID, generation, token, "MAINTENANCE_INVALID", "Invalid maintenance host reservation")
		}
		for key, value := range map[string]any{"maintenance_test_job": true, "maintenance_identity_source": "host", "maintenance_job_id": jobID} {
			_ = job.Set(key, value)
		}
		if err = Write(path, job); err != nil {
			return nil, err
		}
	} else if !errors.Is(markerErr, os.ErrNotExist) {
		return nil, markerErr
	}
	if !research && (worktreePath != "" || argsMap["worktree"] == true) {
		stateRoot := opt.StateRoot
		if stateRoot == "" {
			stateRoot = filepath.Dir(filepath.Dir(dir))
		}
		var repoRoot, relativeCWD string
		if worktreePath != "" {
			var recordedRoot string
			job.Get("repoRoot", &recordedRoot)
			repoRoot = recordedRoot
			if repoRoot == "" {
				repoRoot = worktreePath
			}
			if info, statErr := os.Stat(worktreePath); statErr != nil || !info.IsDir() {
				return finishHostError(path, jobID, generation, token, "WORKTREE_MISSING", "Job worktree is unavailable")
			}
			requested, _ := filepath.Abs(args.Cwd)
			root, _ := filepath.Abs(repoRoot)
			relativeCWD, _ = filepath.Rel(root, requested)
			if relativeCWD == ".." || strings.HasPrefix(relativeCWD, ".."+string(filepath.Separator)) {
				relativeCWD = ""
			}
			var pending bool
			job.Get("replySnapshotPending", &pending)
			if pending {
				var resumed bool
				job.Get("resumedAfterInterruption", &resumed)
				if resumed {
					return finishHostError(path, jobID, generation, token, "WORKTREE_SNAPSHOT_INTERRUPTED", "Worktree snapshot was interrupted")
				}
				var baseline any
				if job.Get("baselineSnapshot", &baseline) {
					_ = job.Set("snapshot", baseline)
				}
				job.Delete("replySnapshotPending")
				if err = Write(path, job); err != nil {
					return nil, err
				}
			}
		} else {
			requested, absErr := filepath.Abs(args.Cwd)
			if absErr != nil {
				return finishHostError(path, jobID, generation, token, "WORKTREE_SNAPSHOT_FAILED", fmt.Sprintf("Worktree snapshot could not be created: resolving requested directory: %v", absErr))
			}
			repoRoot, err = ledger.GitTopLevel(requested)
			if err != nil {
				return finishHostError(path, jobID, generation, token, "WORKTREE_SNAPSHOT_FAILED", fmt.Sprintf("Worktree snapshot could not be created: resolving Git top-level: %v", err))
			}
			relativeCWD, _ = filepath.Rel(repoRoot, requested)
			snapshot, createErr := ledger.CreateJobWorktree(repoRoot, jobID, filepath.Join(stateRoot, "wt"))
			if createErr != nil {
				return finishHostError(path, jobID, generation, token, "WORKTREE_SNAPSHOT_FAILED", fmt.Sprintf("Worktree snapshot could not be created: creating Git worktree snapshot: %v", createErr))
			}
			worktreePath = snapshot["path"]
			threadID := ""
			_ = job.Get("threadId", &threadID)
			attached, attachErr := retention.AttachWorktree(stateRoot, worktreePath, jobID, threadID)
			if attachErr != nil || !attached {
				return finishHostError(path, jobID, generation, token, "WORKTREE_RETIRING", "Thread worktree is being retired")
			}
			job, err = Read(path)
			if err != nil {
				return nil, err
			}
			setupResult := ledger.WorktreeSetupResult{}
			if maintenanceRun == nil {
				setupResult = ledger.RunWorktreeSetup(worktreePath, profile.LoadProfile(repoRoot).Worktree)
			}
			if setupResult.Failed() {
				if err = job.Set("worktreePath", worktreePath); err != nil {
					return nil, err
				}
				if err = job.Set("repoRoot", repoRoot); err != nil {
					return nil, err
				}
				_ = job.Set("setupError", setupResult.Failure)
				if err = Write(path, job); err != nil {
					return nil, err
				}
				return finishHostError(path, jobID, generation, token, "WORKTREE_SETUP_FAILED", "Worktree setup failed")
			}
			for key, value := range map[string]any{"worktreePath": snapshot["path"], "snapshot": snapshot["snapshot"], "baselineSnapshot": snapshot["snapshot"], "head": snapshot["head"], "repoRoot": repoRoot} {
				if err = job.Set(key, value); err != nil {
					return nil, err
				}
			}
			if maintenanceRun == nil {
				resumePartialAttempt(dir, job, worktreePath, &prompt)
			}
			if err = Write(path, job); err != nil {
				return nil, err
			}
		}
		mapped := filepath.Join(worktreePath, relativeCWD)
		if info, statErr := os.Stat(mapped); statErr != nil || !info.IsDir() {
			_ = job.Set("cwdFallback", relativeCWD)
			mapped = worktreePath
			if err = Write(path, job); err != nil {
				return nil, err
			}
		}
		args.Cwd = mapped
		prompt += "\nUse the job worktree as project root; map absolute checkout paths to the same relative path here."
	}
	if research {
		stateRoot := opt.StateRoot
		if stateRoot == "" {
			stateRoot = filepath.Dir(filepath.Dir(dir))
		}
		if repoRoot == "" {
			return finishHostError(path, jobID, generation, token, "CODEX_FAILED", "Research scratch must be outside the repository")
		}
		researchRoot := filepath.Join(stateRoot, "research")
		if pathWithin(repoRoot, stateRoot) || pathsOverlap(repoRoot, filepath.Join(researchRoot, jobID)) {
			args.Sandbox = "read-only"
		} else if !pathWithin(researchRoot, args.Cwd) || pathsOverlap(repoRoot, args.Cwd) {
			args.Cwd, err = prepareResearchScratch(stateRoot, jobID, repoRoot)
			if err != nil {
				return finishHostError(path, jobID, generation, token, "CODEX_FAILED", "Research scratch directory is unavailable")
			}
		}
	}
	if agent == "codex" {
		stateRoot := opt.StateRoot
		if stateRoot == "" {
			stateRoot = filepath.Dir(filepath.Dir(dir))
		}
		cacheUser, cacheErr := acquireCacheUser(stateRoot)
		if cacheErr != nil {
			return finishHostError(path, jobID, generation, token, "CODEX_FAILED", "Codex Go cache is busy")
		}
		defer cacheUser()
		jobTempDir = filepath.Join(stateRoot, "cache", "tmp", jobID)
		var writableRoots []any
		if research {
			writableRoots = append(writableRoots, args.Cwd)
		} else if worktreePath != "" {
			writableRoots = append(writableRoots, worktreePath)
		}
		protectedRoot := ""
		if research {
			protectedRoot = repoRoot
		}
		if err = configureCodexGoCache(&args, &childEnv, stateRoot, jobTempDir, writableRoots, research, protectedRoot); err != nil {
			return finishHostError(path, jobID, generation, token, "CODEX_FAILED", "Codex Go cache directory is unavailable")
		}
		if research && args.Sandbox == "read-only" {
			jobTempDir = ""
		}
	}
	if launch.CodexBin != nil {
		encoded, _ := json.Marshal(launch.CodexBin)
		args.BinaryOverride = string(encoded)
	}
	var commandArgs []string
	if agent == "codex" {
		commandArgs, err = BuildCodexCommand(args)
	} else {
		configArgs, configErr := FlattenConfig(args.Config)
		if configErr != nil {
			err = configErr
		} else {
			var prefix []string
			if launch.CodexBin != nil {
				prefix, err = ResolveAgentPrefix(agent, launch.CodexBin)
			} else {
				prefix, err = ResolveAgentPrefix(agent, nil)
			}
			if err == nil {
				var sandbox *string
				if args.Sandbox != "" {
					sandbox = &args.Sandbox
				}
				input := AgentCommandInput{Agent: agent, Prefix: prefix, Tool: launch.Name, CWD: args.Cwd, Model: args.Model, ConfigArgs: configArgs, Sandbox: sandbox, ThreadID: args.ThreadID, Prompt: prompt}
				commandArgs, err = BuildAgentCommand(input)
				if err == nil {
					childEnv = append(childEnv, AgentCommandEnvironment(input)...)
				}
			}
		}
	}
	if opt.WorkerCommand != nil {
		commandArgs, err = opt.WorkerCommand(args)
	}
	if err != nil || len(commandArgs) == 0 {
		return finishHostError(path, jobID, generation, token, "CODEX_FAILED", "Codex launch arguments are invalid")
	}
	if leaseQueued {
		root := filepath.Dir(filepath.Dir(dir))
		if err = WaitForQueuedBaseline(ctx, root, jobID, time.Second); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				_ = finishJob(dir, jobID, "TIMED_OUT", "DEADLINE_EXCEEDED", nil, "Codex deadline exceeded", float64(time.Now().UnixNano())/1e9, hostFinishOptions(generation, token))
				return map[string]any{"status": "ok"}, nil
			}
			if errors.Is(err, context.Canceled) {
				_ = finishJob(dir, jobID, "CANCELLED", "CANCELLED", nil, "Codex cancelled", float64(time.Now().UnixNano())/1e9, hostFinishOptions(generation, token))
				return map[string]any{"status": "ok"}, nil
			}
			message := "Fresh admission baseline could not be bound; FULL review required: " + err.Error()
			_ = finishJob(dir, jobID, "FAILED", "ADMISSION_BASELINE_UNAVAILABLE", nil, message, float64(time.Now().UnixNano())/1e9, hostFinishOptions(generation, token))
			return map[string]any{"status": "error", "errorCode": "ADMISSION_BASELINE_UNAVAILABLE", "message": message}, nil
		}
	}
	if cancelled.Load() {
		return map[string]any{"status": "ok"}, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || float64(time.Now().UnixNano())/1e9 >= deadline {
		_ = finishJob(dir, jobID, "TIMED_OUT", "DEADLINE_EXCEEDED", nil, "Codex deadline exceeded", float64(time.Now().UnixNano())/1e9, hostFinishOptions(generation, token))
		return map[string]any{"status": "ok"}, nil
	}
	if ctx.Err() != nil {
		return map[string]any{"status": "ok"}, nil
	}
	now := float64(time.Now().UnixNano()) / 1e9
	lock, err := AcquireOwnership(dir, jobID)
	if err != nil {
		return nil, err
	}
	job, err = Read(path)
	if err == nil {
		job.Get("jobId", &recordedID)
		job.Get("generation", &recordedGeneration)
		job.Get("ownershipToken", &recordedToken)
		job.Get("state", &state)
		if recordedID != jobID || recordedGeneration != generation || recordedToken != token || state != "QUEUED" {
			_ = lock.Release()
			return map[string]any{"status": "stale"}, nil
		}
		now = float64(time.Now().UnixNano()) / 1e9
		_ = job.Set("state", "RUNNING")
		_ = job.Set("startedAt", now)
		err = Write(path, job)
	}
	if releaseErr := lock.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return nil, err
	}
	if maintenanceRun != nil {
		if err = prepareMaintenanceTests(ctx, path, worktreePath, maintenanceRun); err != nil {
			return finishHostStartError(dir, jobID, generation, token, cancelled.Load(), ctx.Err(), err)
		}
	}
	var result EventResult
	var failureEvidence map[string]any
	for {
		command := proc.Hide(exec.CommandContext(ctx, commandArgs[0], commandArgs[1:]...))
		if len(childEnv) != 0 {
			command.Env = os.Environ()
			for _, override := range childEnv {
				key, _, _ := strings.Cut(override, "=")
				found := false
				for index, entry := range command.Env {
					entryKey, _, _ := strings.Cut(entry, "=")
					if strings.EqualFold(entryKey, key) {
						command.Env[index] = override
						found = true
					}
				}
				if !found {
					command.Env = append(command.Env, override)
				}
			}
		}
		command.Dir = args.Cwd
		command.Stdin = strings.NewReader(prompt)
		stream := NewEventStream(agent, args.ThreadID)
		activity := NewLivenessActivity(job, agent, StallThreshold(dir, job), now)
		activityMu := &sync.Mutex{}
		command.Stdout = eventWriter{stream: stream, activity: activity, activityMu: activityMu}
		stderr := &tailBuffer{limit: 4000}
		command.Stderr = stderr
		var supervised *supervisedCommand
		var treeResult proc.SupervisionResult
		lock, err = AcquireOwnership(dir, jobID)
		if err != nil {
			return nil, err
		}
		job, err = Read(path)
		if err == nil {
			job.Get("generation", &recordedGeneration)
			job.Get("ownershipToken", &recordedToken)
			job.Get("state", &state)
			if recordedGeneration != generation || recordedToken != token || state != "RUNNING" {
				_ = lock.Release()
				return map[string]any{"status": "stale"}, nil
			}
			var previousReceipt string
			job.Get("treeReceipt", &previousReceipt)
			receipt, receiptErr := prepareTreeReceipt(dir, jobID, previousReceipt)
			if receiptErr == nil {
				_ = job.Set("treeReceipt", receipt)
				receiptErr = Write(path, job)
			}
			if receiptErr != nil {
				_ = lock.Release()
				return nil, receiptErr
			}
			supervised, err = startSupervisedCommand(ctx, command, receipt)
			if err != nil {
				_ = lock.Release()
				if proofErr := writeTreeReceipt(receipt, true, err); proofErr != nil {
					return nil, errors.Join(err, proofErr)
				}
				close(terminal)
				contextErr := ctx.Err()
				if contextErr == nil && float64(time.Now().UnixNano())/1e9 >= deadline {
					contextErr = context.DeadlineExceeded
				}
				return finishHostStartError(dir, jobID, generation, token, cancelled.Load(), contextErr, err)
			}
			workerAlive = true
			_ = job.Set("pid", supervised.GuardPID)
			err = Write(path, job)
		}
		if releaseErr := lock.Release(); err == nil {
			err = releaseErr
		}
		if err != nil {
			if supervised != nil {
				supervised.Stop()
				treeResult, stopErr := supervised.wait()
				workerAlive = !treeResult.Confirmed
				err = errors.Join(err, stopErr)
			}
			return nil, err
		}
		activityMu.Lock()
		_, checkpointErr := CheckpointLiveness(path, generation, token, activity, float64(time.Now().UnixNano())/1e9, true, true)
		activityMu.Unlock()
		if checkpointErr != nil {
			supervised.Stop()
			treeResult, stopErr := supervised.wait()
			workerAlive = !treeResult.Confirmed
			return nil, errors.Join(checkpointErr, stopErr)
		}
		done := make(chan error, 1)
		go func() { var waitErr error; treeResult, waitErr = supervised.wait(); done <- waitErr }()
		checkTicker := time.NewTicker(100 * time.Millisecond)
		defer checkTicker.Stop()
		var runErr error
		for running := true; running; {
			select {
			case runErr = <-done:
				running = false
			case <-checkTicker.C:
				activityMu.Lock()
				_, checkpointErr := CheckpointLiveness(path, generation, token, activity, float64(time.Now().UnixNano())/1e9, true, false)
				activityMu.Unlock()
				if checkpointErr != nil {
					supervised.Stop()
					stopErr := <-done
					workerAlive = !treeResult.Confirmed
					return nil, errors.Join(checkpointErr, stopErr)
				}
			}
		}
		workerAlive = !treeResult.Confirmed
		checkTicker.Stop()
		if !treeResult.Confirmed {
			message := "Process termination unconfirmed"
			if runErr != nil {
				message = runErr.Error()
			}
			return finishHostError(path, jobID, generation, token, "PROCESS_UNCONFIRMED", message)
		}
		if treeResult.RootExit == -1 {
			close(terminal)
			return finishHostStartError(dir, jobID, generation, token, cancelled.Load(), ctx.Err(), runErr)
		}
		if !cancelled.Load() {
			activityMu.Lock()
			lastActivity := numberOr(activity["lastActivityAt"], float64(time.Now().UnixNano())/1e9)
			activity["longestSilentGapSeconds"] = math.Max(numberOr(activity["longestSilentGapSeconds"], 0), math.Max(0, float64(time.Now().UnixNano())/1e9-lastActivity))
			_, checkpointErr = CheckpointLiveness(path, generation, token, activity, float64(time.Now().UnixNano())/1e9, false, true)
			activityMu.Unlock()
			if checkpointErr != nil {
				return nil, checkpointErr
			}
		}
		exitCode := treeResult.RootExit
		if runErr != nil && exitCode == 0 {
			exitCode = 1
		}
		if cancelled.Load() || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			exitCode = 1
		}
		lock, err = AcquireOwnership(dir, jobID)
		if err == nil {
			job, err = Read(path)
			if err == nil {
				_ = job.Set("exitCode", exitCode)
				events := stream.Events()
				if evidence := TestEvidence(events); len(evidence) > 0 {
					_ = job.Set("testEvidence", evidence)
				}
				_ = job.Set("testEvidenceFailed", TestEvidenceFailed(events))
				err = Write(path, job)
			}
			if releaseErr := lock.Release(); err == nil {
				err = releaseErr
			}
		}
		if err != nil {
			return nil, err
		}
		result = stream.Result(cancelled.Load() || ctx.Err() != nil, exitCode, stderr.String())
		if cancelled.Load() || ctx.Err() != nil {
			break
		}
		failureEvidence = LaunchFailureEvidence(agent, stream.Events(), stderr.String(), true,
			numberOr(activity["stdoutBytes"], 0) > 0, hostOutputHasData(args.OutputPath), &exitCode)
		if err = saveHostAttemptFields(path, jobID, generation, token, result.Status != "success" || result.ThreadID == "", failureEvidence); err != nil {
			return nil, err
		}
		if result.Status == "success" {
			break
		}
		delay, retry, retryErr := reserveCapacityRetry(path, jobID, generation, token, failureEvidence)
		if retryErr != nil {
			return nil, retryErr
		}
		if !retry {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if cancelled.Load() || ctx.Err() != nil {
			break
		}
	}
	if result.ThreadID != "" {
		configArgs, configErr := FlattenConfig(args.Config)
		if configErr != nil {
			return finishHostError(path, jobID, generation, token, "CODEX_FAILED", "Codex launch configuration is invalid")
		}
		if configArgs == nil {
			configArgs = []string{}
		}
		var model, sandbox any
		if args.Model != "" {
			model = args.Model
		}
		if args.Sandbox != "" {
			sandbox = args.Sandbox
		}
		threadConfig := map[string]any{"cwd": args.Cwd, "model": model, "config_args": configArgs,
			"sandbox": sandbox, "agent": agent}
		for source, target := range map[string]string{"taskId": "task_id", "route": "route", "model": "model", "effort": "effort"} {
			var value any
			if job.Get(source, &value) && value != nil {
				threadConfig[target] = value
			}
		}
		for _, key := range []string{"worktreePath", "snapshot", "baselineSnapshot", "head", "repoRoot"} {
			var value any
			if job.Get(key, &value) {
				threadConfig[key] = value
			}
		}
		if err = saveHostThread(dir, jobID, generation, token, result.ThreadID, threadConfig, launch.Name == "codex", opt.StateRoot); err != nil {
			return nil, err
		}
	}
	finished := float64(time.Now().UnixNano()) / 1e9
	switch {
	case cancelled.Load():
		err = finishJob(dir, jobID, "CANCELLED", "CANCELLED", nil, "Codex cancelled", finished, hostFinishOptions(generation, token))
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = finishJob(dir, jobID, "TIMED_OUT", "DEADLINE_EXCEEDED", nil, "Codex deadline exceeded", finished, hostFinishOptions(generation, token))
	case result.Status != "success" || result.ThreadID == "":
		message := result.Failure
		if message == "" {
			message = "Codex completion event missing"
		}
		message = "Codex failed: " + message
		code := "CODEX_FAILED"
		if attempts, valid := recoveryInteger(job, "capacityRetryAttempts"); valid && attempts >= 2 &&
			failureEvidence["type"] == "RATE_LIMITED" && failureEvidence["noWorkProof"] == true {
			code = "CAPACITY_RETRIES_EXHAUSTED"
			message += "; automatic capacity retries exhausted"
		}
		err = finishJob(dir, jobID, "FAILED", code, nil, message, finished, hostFinishOptions(generation, token))
	default:
		if maintenanceRun != nil {
			if verifyErr := verifyMaintenanceTests(ctx, path, worktreePath, maintenanceRun); verifyErr != nil {
				status, code := "FAILED", "MAINTENANCE_PATCH_REJECTED"
				if cancelled.Load() {
					status, code = "CANCELLED", "CANCELLED"
				} else if ctx.Err() != nil {
					status, code = "TIMED_OUT", "DEADLINE_EXCEEDED"
				}
				err = finishJob(dir, jobID, status, code, nil, verifyErr.Error(), float64(time.Now().UnixNano())/1e9, hostFinishOptions(generation, token))
				break
			}
		}
		content := result.Final
		if data, readErr := os.ReadFile(args.OutputPath); readErr == nil {
			content = string(data)
		}
		jobResult := map[string]any{
			"content": content, "threadId": result.ThreadID, "usage": result.Usage,
		}
		if len(result.Notes) > 0 {
			jobResult["notes"] = result.Notes
		}
		checkReadinessBeforeSuccess(dir, jobID, jobResult)
		finished = float64(time.Now().UnixNano()) / 1e9
		err = finishJob(dir, jobID, "SUCCEEDED", "", jobResult, "", finished, hostFinishOptions(generation, token))
	}
	close(terminal)
	time.Sleep(200 * time.Millisecond)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok"}, nil
}

// MaintenanceTestRun is host evidence; workers cannot write its durable record.
type MaintenanceTestRun struct {
	JobID, BaselineHead, BaselineSnapshot string
	Target                                float64
	Before, After                         testcoverage.Evidence
	KnownTests, AddedTests, ExecutedTests []string
	Revision, BeforeOutput, AfterOutput   string
	BeforeCoverage, AfterCoverage         string
	NewTests                              int
}

func prepareMaintenanceTests(ctx context.Context, path, worktree string, run *MaintenanceTestRun) error {
	job, err := Read(path)
	if err != nil {
		return err
	}
	var head, snapshot string
	job.Get("head", &head)
	job.Get("snapshot", &snapshot)
	if worktree == "" || head != run.BaselineHead || snapshot != run.BaselineSnapshot {
		return errors.New("maintenance worktree baseline moved")
	}
	deadline, _ := ctx.Deadline()
	run.Before = testcoverage.MeasureProject(ctx, worktree, time.Until(deadline))
	if err = job.Set("maintenanceTests", run); err != nil {
		return err
	}
	if err = Write(path, job); err != nil {
		return err
	}
	if run.Before.Status != "measured" {
		return ErrCoverageUnknown
	}
	if run.Target <= 0 {
		run.Target = 90
	}
	if 100*float64(run.Before.Counts.Covered)/float64(run.Before.Counts.Executable) >= run.Target {
		return errors.New("maintenance coverage target already reached")
	}
	var repository string
	job.Get("repoRoot", &repository)
	artifacts := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(path))), "projects", contextio.ProjectKey(repository), "maintenance", "reports", "test-gaps", "artifacts", run.JobID)
	if err = os.MkdirAll(artifacts, 0700); err != nil {
		return err
	}
	run.BeforeCoverage = filepath.Join(artifacts, "before.coverprofile")
	if err = atomicWrite(run.BeforeCoverage, []byte(run.Before.Raw)); err != nil {
		return err
	}
	run.Before.Raw = ""
	run.BeforeOutput = filepath.Join(artifacts, "before-tests.jsonl")
	run.KnownTests, err = maintenanceGoTests(ctx, worktree, run.Before.Scope, run.BeforeOutput, true)
	if err != nil {
		return err
	}
	delta := ledger.ComputeJobDelta(worktree, snapshot)
	if delta["status"] != "OK" || delta["changed_file_count"] != 0 {
		return errors.New("baseline measurement changed the worktree")
	}
	return nil
}

func verifyMaintenanceTests(ctx context.Context, path, worktree string, run *MaintenanceTestRun) error {
	delta := ledger.ComputeJobDelta(worktree, run.BaselineSnapshot)
	identity := MaintenanceTestIdentity{JobID: run.JobID, RecordedID: run.JobID, RecordedBy: "host"}
	if err := ValidateMaintenanceTestDelta(worktree, delta, identity); err != nil {
		return err
	}
	if delta["current_head"] != run.BaselineHead {
		return errors.New("maintenance worker changed HEAD")
	}
	revision, err := ledger.JobChangedPathsSHA256(worktree, run.BaselineSnapshot)
	if err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	run.After = testcoverage.MeasureProject(ctx, worktree, time.Until(deadline))
	run.AfterCoverage = filepath.Join(filepath.Dir(run.BeforeOutput), "after.coverprofile")
	if err = atomicWrite(run.AfterCoverage, []byte(run.After.Raw)); err != nil {
		return err
	}
	run.After.Raw = ""
	if !testcoverage.Compare(run.Before, run.After) {
		return errors.New("maintenance patch has no measured coverage improvement")
	}
	run.AfterOutput = filepath.Join(filepath.Dir(run.BeforeOutput), "after-tests.jsonl")
	run.ExecutedTests, err = maintenanceGoTests(ctx, worktree, run.After.Scope, run.AfterOutput, false)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, name := range run.KnownTests {
		known[name] = true
	}
	for _, name := range run.ExecutedTests {
		if !known[name] {
			run.AddedTests = append(run.AddedTests, name)
		}
	}
	run.NewTests = CountExecutedMaintenanceTests(run.AddedTests, run.ExecutedTests)
	if run.NewTests == 0 {
		return errors.New("maintenance patch executed no new tests")
	}
	current, err := ledger.JobChangedPathsSHA256(worktree, run.BaselineSnapshot)
	if err != nil || current != revision || ctx.Err() != nil {
		return errors.New("maintenance patch changed or deadline expired during verification")
	}
	run.Revision = revision
	job, err := Read(path)
	if err != nil {
		return err
	}
	if err = job.Set("maintenanceTests", run); err != nil {
		return err
	}
	return Write(path, job)
}

func maintenanceGoTests(ctx context.Context, worktree, scope, outputPath string, list bool) ([]string, error) {
	module := strings.SplitN(scope, "\n", 2)[0]
	args := []string{"test", "-json", "-mod=readonly", "-count=1"}
	if list {
		args = append(args, "-list=^Test")
	}
	command := proc.Hide(exec.CommandContext(ctx, "go", append(args, "./...")...))
	command.Dir = filepath.Join(worktree, filepath.FromSlash(module))
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	proc.ConfigureProcessTreeKill(command)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	if err = atomicWrite(outputPath, output); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	seen := map[string]bool{}
	var names []string
	for {
		var event struct{ Action, Package, Test, Output string }
		if err = decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		name := event.Test
		if list && event.Action == "output" {
			name = strings.TrimSpace(event.Output)
		}
		if (!list && event.Action != "pass") || !strings.HasPrefix(name, "Test") || strings.ContainsAny(name, "/ \t\r\n") || event.Package == "" {
			continue
		}
		identity := event.Package + "/" + name
		if !seen[identity] {
			names = append(names, identity)
			seen[identity] = true
		}
	}
	return names, nil
}

func configureCodexGoCache(args *LaunchArgs, childEnv *[]string, stateRoot, jobTempDir string, writableRoots []any, research bool, protectedRoot string) error {
	// Enable research writes only after all writable paths pass the overlap checks.
	if research {
		args.Sandbox = "read-only"
	}
	config := map[string]any{}
	switch existing := args.Config.(type) {
	case OrderedObject:
		for _, entry := range existing {
			config[entry.Key] = entry.Value
		}
	case map[string]any:
		config = existing
	}
	workspaceValue := config["sandbox_workspace_write"]
	var existingRoots any
	switch workspace := workspaceValue.(type) {
	case map[string]any:
		existingRoots = workspace["writable_roots"]
	case []ConfigEntry:
		for _, entry := range workspace {
			if entry.Key == "writable_roots" {
				existingRoots = entry.Value
				break
			}
		}
	case OrderedObject:
		for _, entry := range workspace {
			if entry.Key == "writable_roots" {
				existingRoots = entry.Value
				break
			}
		}
	}
	// A job worktree is the only project path the worker may write. Caller
	// supplied roots can include the main checkout, whose changes are not part
	// of the worktree delta inspected after the job.
	if len(writableRoots) == 0 {
		if existing, ok := existingRoots.([]any); ok {
			writableRoots = append(writableRoots, existing...)
		}
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		telemetryDir := filepath.Join(configDir, "go", "telemetry")
		if info, err := os.Stat(telemetryDir); err == nil && info.IsDir() {
			writableRoots = append(writableRoots, telemetryDir)
		}
	}
	if research && protectedRoot != "" {
		if pathsOverlap(protectedRoot, args.Cwd) || pathsOverlap(protectedRoot, jobTempDir) {
			return nil
		}
		for _, root := range writableRoots {
			if value, ok := root.(string); ok && pathsOverlap(protectedRoot, value) {
				return nil
			}
		}
	}
	if err := os.MkdirAll(jobTempDir, 0700); err != nil {
		return err
	}
	for _, name := range []string{"TEMP", "TMP", "TMPDIR"} {
		*childEnv = append(*childEnv, name+"="+jobTempDir)
	}
	for _, variable := range []struct{ name, directory string }{
		{"GOCACHE", filepath.Join(stateRoot, "cache", "go-build")},
	} {
		if os.Getenv(variable.name) != "" {
			continue
		}
		if research && protectedRoot != "" && pathsOverlap(protectedRoot, variable.directory) {
			return nil
		}
		if err := os.MkdirAll(variable.directory, 0700); err != nil {
			return err
		}
		writableRoots = append(writableRoots, variable.directory)
		*childEnv = append(*childEnv, variable.name+"="+variable.directory)
	}
	writableRoots = append(writableRoots, jobTempDir)
	if research && protectedRoot != "" {
		for _, root := range writableRoots {
			if value, ok := root.(string); ok && pathsOverlap(protectedRoot, value) {
				return nil
			}
		}
		if pathsOverlap(protectedRoot, args.Cwd) {
			return nil
		}
	}
	if os.Getenv("GOTMPDIR") == "" {
		*childEnv = append(*childEnv, "GOTMPDIR="+jobTempDir)
	}
	for key := range config {
		if strings.HasPrefix(key, "sandbox_workspace_write.") {
			delete(config, key)
		}
	}
	if len(writableRoots) > 0 {
		workspace := map[string]any{"writable_roots": writableRoots}
		if research {
			workspace["network_access"] = true
			workspace["exclude_slash_tmp"] = true
			workspace["exclude_tmpdir_env_var"] = true
			args.Sandbox = "workspace-write"
		}
		config["sandbox_workspace_write"] = workspace
	}
	args.Config = config
	return nil
}

func pathWithin(root, path string) bool {
	root, rootErr := resolvedPath(root)
	path, pathErr := resolvedPath(path)
	if rootErr != nil || pathErr != nil {
		return true
	}
	if !strings.EqualFold(filepath.VolumeName(root), filepath.VolumeName(path)) {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func resolvedPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		if _, err = os.Lstat(path); err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return "", resolveErr
			}
			path = resolved
			for index := len(suffix) - 1; index >= 0; index-- {
				path = filepath.Join(path, suffix[index])
			}
			return filepath.Clean(path), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}

func pathsOverlap(first, second string) bool {
	return pathWithin(first, second) || pathWithin(second, first)
}

func prepareResearchScratch(stateRoot, jobID, repoRoot string) (string, error) {
	directory := filepath.Join(stateRoot, "research", jobID)
	if pathsOverlap(repoRoot, directory) {
		return "", errors.New("research scratch overlaps repository")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	if os.PathSeparator != '\\' {
		if err := os.Chmod(directory, 0700); err != nil {
			return "", err
		}
	}
	return directory, nil
}

func finishHostStartError(dir, id string, generation int64, token string, cancelled bool, contextErr, startErr error) (map[string]any, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	switch {
	case cancelled:
		if err := finishJob(dir, id, "CANCELLED", "CANCELLED", nil, "Codex cancelled", now, hostFinishOptions(generation, token)); err != nil {
			return nil, err
		}
		return map[string]any{"status": "ok"}, nil
	case errors.Is(contextErr, context.DeadlineExceeded):
		if err := finishJob(dir, id, "TIMED_OUT", "DEADLINE_EXCEEDED", nil, "Codex deadline exceeded", now, hostFinishOptions(generation, token)); err != nil {
			return nil, err
		}
		return map[string]any{"status": "ok"}, nil
	default:
		code := "CODEX_FAILED"
		if errors.Is(startErr, ErrCoverageUnknown) {
			code = "COVERAGE_UNKNOWN"
		}
		if err := finishJob(dir, id, "FAILED", code, nil, startErr.Error(), now, hostFinishOptions(generation, token)); err != nil {
			return nil, err
		}
		return map[string]any{"status": "error", "errorCode": code, "message": startErr.Error()}, nil
	}
}

func saveHostAttemptFields(path, id string, generation int64, token string, failed bool, evidence map[string]any) error {
	lock, err := AcquireOwnership(filepath.Dir(path), id)
	if err != nil {
		return err
	}
	job, err := Read(path)
	if err == nil {
		var actual int64
		var owner, state string
		job.Get("generation", &actual)
		job.Get("ownershipToken", &owner)
		job.Get("state", &state)
		if actual == generation && owner == token && state == "RUNNING" {
			attempts, _ := recoveryInteger(job, "capacityRetryAttempts")
			_ = job.Set("responsivenessAttempts", attempts+1)
			if failed {
				_ = job.Set("launchFailureEvidence", evidence)
			} else {
				job.Delete("launchFailureEvidence")
			}
			err = Write(path, job)
		}
	}
	if releaseErr := lock.Release(); err == nil {
		err = releaseErr
	}
	return err
}

func hostOutputHasData(path string) bool {
	data, err := os.ReadFile(path)
	return len(data) > 0 || err != nil && !errors.Is(err, os.ErrNotExist)
}

type eventWriter struct {
	stream     *EventStream
	activity   LivenessActivity
	activityMu *sync.Mutex
}

func (w eventWriter) Write(value []byte) (int, error) {
	w.stream.Write(value)
	if w.activityMu != nil {
		w.activityMu.Lock()
		w.activity["stdoutBytes"] = int(numberOr(w.activity["stdoutBytes"], 0)) + len(value)
		RecordLivenessActivity(w.activity, float64(time.Now().UnixNano())/1e9)
		w.activityMu.Unlock()
	}
	return len(value), nil
}

type tailBuffer struct {
	data  []byte
	limit int
}

func (b *tailBuffer) Write(value []byte) (int, error) {
	b.data = append(b.data, value...)
	if len(b.data) > b.limit {
		b.data = append(b.data[:0], b.data[len(b.data)-b.limit:]...)
	}
	return len(value), nil
}

func (b *tailBuffer) String() string { return string(b.data) }

func numericDeadline(job *Record) (float64, bool) {
	var value float64
	if !job.Get("deadlineAt", &value) || value <= 0 {
		return 0, false
	}
	return value, true
}

func hostFinishOptions(generation int64, token string) FinishOptions {
	return FinishOptions{Generation: &generation, Token: token}
}

func saveHostThread(dir, id string, generation int64, token, threadID string, config map[string]any, saveRecord bool, stateRoot string) error {
	lock, err := AcquireOwnership(dir, id)
	if err != nil {
		return err
	}
	owned := false
	job, err := Read(filepath.Join(dir, id+".json"))
	if err == nil {
		var actual int64
		var owner, state string
		job.Get("generation", &actual)
		job.Get("ownershipToken", &owner)
		job.Get("state", &state)
		if actual == generation && owner == token && state == "RUNNING" {
			owned = true
			err = job.Set("threadId", threadID)
			if err == nil {
				err = job.Set("threadConfig", config)
			}
			if err == nil {
				err = Write(filepath.Join(dir, id+".json"), job)
			}
		}
	}
	if releaseErr := lock.Release(); err == nil {
		err = releaseErr
	}
	if err != nil || !owned || !saveRecord {
		return err
	}
	if worktree := nonemptyString(config["worktreePath"]); worktree != "" {
		if stateRoot == "" {
			stateRoot = filepath.Dir(filepath.Dir(dir))
		}
		attached, attachErr := retention.AttachWorktree(stateRoot, worktree, id, threadID)
		if attachErr != nil || !attached {
			return fmt.Errorf("thread worktree attachment was rejected")
		}
	}
	record := map[string]any{"thread_id": threadID}
	for key, value := range config {
		record[key] = value
	}
	return SaveRecord(filepath.Join(filepath.Dir(filepath.Dir(dir)), "codex_exec_mcp", "threads"), record)
}

func failHostUnavailable(path, id string, generation int64, token, message string) (result map[string]any, resultErr error) {
	lock, err := AcquireOwnership(filepath.Dir(path), id)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Release()) }()
	job, err := Read(path)
	if err != nil {
		return nil, err
	}
	var actual int64
	var owner, state string
	job.Get("generation", &actual)
	job.Get("ownershipToken", &owner)
	job.Get("state", &state)
	if actual != generation || owner != token || state != "QUEUED" {
		return map[string]any{"status": "stale"}, nil
	}
	_ = job.Set("state", "FAILED")
	_ = job.Set("finishedAt", float64(time.Now().UnixNano())/1e9)
	_ = job.Set("errorCode", hostUnavailableCode)
	_ = job.Set("message", message)
	var displayCount any
	if !job.Get("boardDisplayCount", &displayCount) {
		_ = job.Set("boardDisplayCount", 0)
	}
	if err = Write(path, job); err != nil {
		return nil, err
	}
	finishFailedAttempt(filepath.Dir(path), id, job)
	_ = os.Remove(filepath.Join(filepath.Dir(path), id+".launch"))
	return map[string]any{"status": "error", "errorCode": hostUnavailableCode, "message": message}, nil
}

func finishHostError(path, id string, generation int64, token, code, message string) (map[string]any, error) {
	state := "FAILED"
	if code == "PROCESS_UNCONFIRMED" {
		state = "UNKNOWN_OR_IN_PROGRESS"
	}
	err := finishJob(filepath.Dir(path), id, state, code, nil, message,
		float64(time.Now().UnixNano())/1e9, hostFinishOptions(generation, token))
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "error", "errorCode": code, "message": message}, nil
}
