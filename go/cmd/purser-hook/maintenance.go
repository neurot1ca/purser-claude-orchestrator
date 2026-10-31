package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/jobs"
	"purser-hook/internal/lease"
	"purser-hook/internal/maintenance"
	"purser-hook/internal/pluginroot"
	"purser-hook/internal/proc"
)

type maintenanceJobs struct{}

var maintenanceStartDispatch = dispatchOperation
var maintenanceStartHook = maintenanceHook

func (maintenanceJobs) Start(ctx context.Context, state string, args map[string]any) (result map[string]any, err error) {
	if args["worktree"] != true {
		return dispatchOperation(ctx, "codex_start", args, state)
	}
	root, _ := args["cwd"].(string)
	id, _ := args["call"].(string)
	event := map[string]any{"session_id": "maintenance-" + contextio.ProjectKey(root), "tool_use_id": id, "cwd": root,
		"hook_event_name": "PreToolUse", "tool_name": "mcp__codex-main__codex_start", "tool_input": args}
	scope := []string{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
			continue
		}
		if entry.IsDir() {
			scope = append(scope, name)
		} else if strings.HasSuffix(name, ".go") {
			scope = append(scope, strings.TrimSuffix(strings.TrimSuffix(name, ".go"), "_test")+"_test.go")
		}
	}
	if len(scope) == 0 || strings.ContainsAny(strings.Join(scope, ""), "|,\r\n") {
		return nil, errors.New("maintenance test scope unavailable")
	}
	sort.Strings(scope)
	scope = slices.Compact(scope)
	args["prompt"] = fmt.Sprintf("ORCH|task=maintenance-%s|phase=implementation|route=subtle|scope=%s\nTASK_CONTRACT: {\"objective\":\"Add behavior tests for ranked coverage gaps\",\"criteria\":\"Test-only patch; host coverage improvement; executed new tests; clean independent review\"}\n%s", id, strings.Join(scope, ","), args["prompt"])
	keyBytes := sha256.Sum256([]byte(event["session_id"].(string) + "\x00" + id))
	key := hex.EncodeToString(keyBytes[:])
	jobID := ""
	dispatched := false
	defer func() {
		if err != nil {
			if jobID != "" {
				result = map[string]any{"structuredContent": map[string]any{"jobId": jobID}}
				if !cancelMaintenanceAdmission(state, jobID, dispatched) {
					err = errors.Join(err, maintenance.ErrTestPatchTerminationUnconfirmed)
				}
			}
			if !errors.Is(err, maintenance.ErrTestPatchTerminationUnconfirmed) {
				_ = lease.New(filepath.Join(state, "projects", contextio.ProjectKey(root))).Release(key, float64(time.Now().UnixNano())/1e9)
			}
		}
	}()
	pre, err := maintenanceStartHook(ctx, state, event)
	if err != nil {
		return nil, err
	}
	hook, _ := pre["hookSpecificOutput"].(map[string]any)
	effective, _ := hook["updatedInput"].(map[string]any)
	if hook["permissionDecision"] == "deny" || effective == nil || effective["worktree"] != true {
		return nil, errors.New("maintenance admission denied or enforcement input unavailable")
	}
	call, _ := effective["call"].(string)
	jobHash := sha256.Sum256([]byte(call))
	jobID = hex.EncodeToString(jobHash[:])
	run := jobs.MaintenanceTestRun{JobID: jobID}
	run.BaselineHead, _ = args["maintenanceBaselineHead"].(string)
	run.BaselineSnapshot, _ = args["maintenanceBaselineSnapshot"].(string)
	run.Target, _ = args["coverage_target_pct"].(float64)
	if err = maintenance.ReserveTestJob(state, key, run); err != nil {
		return nil, err
	}
	deadlineText, _ := args["maintenanceDeadlineAt"].(string)
	deadline, err := time.Parse(time.RFC3339Nano, deadlineText)
	if err != nil || time.Until(deadline) <= 0 {
		return nil, errors.New("maintenance admission exhausted deadline")
	}
	effective["timeoutSeconds"] = time.Until(deadline).Seconds()
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	dispatched = true
	result, err = maintenanceStartDispatch(ctx, "codex_start", effective, state)
	if err != nil {
		return nil, err
	}
	event["hook_event_name"], event["tool_input"], event["tool_response"] = "PostToolUse", effective, result
	if _, err = maintenanceStartHook(ctx, state, event); err != nil {
		return nil, err
	}
	if _, err = os.Stat(filepath.Join(state, "async_jobs", jobID+".json")); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result, nil
}

func cancelMaintenanceAdmission(state, id string, dispatched bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	done := make(chan bool, 1)
	go func() {
		for {
			value := jobs.Cancel(state, map[string]any{"jobId": id})
			result, _ := value["structuredContent"].(map[string]any)
			code, status := result["errorCode"], result["state"]
			if code == "JOB_NOT_FOUND" && !dispatched {
				if _, err := os.Stat(filepath.Join(state, "codex_exec_mcp", "jobs", id+".json")); errors.Is(err, os.ErrNotExist) {
					done <- true
					return
				}
			}
			if code != "JOB_NOT_FOUND" && code != "JOB_UPDATE_FAILED" && code != "PROCESS_UNCONFIRMED" {
				switch status {
				case "SUCCEEDED", "FAILED", "CANCELLED", "TIMED_OUT", "SUPERSEDED":
					done <- true
					return
				}
			}
			if !time.Now().Before(deadline) {
				done <- false
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case confirmed := <-done:
		return confirmed
	case <-timer.C:
		return false
	}
}
func (maintenanceJobs) Result(state, id string) map[string]any {
	value, _ := dispatchOperation(context.Background(), "codex_result", map[string]any{"jobId": id}, state)
	return value
}
func (maintenanceJobs) Cancel(state, id string) map[string]any {
	value, _ := dispatchOperation(context.Background(), "codex_cancel", map[string]any{"jobId": id}, state)
	return value
}

// Finalize supervises the normal Post hook, including all reviewer descendants.
func (maintenanceJobs) Finalize(ctx context.Context, state, id string, result map[string]any) error {
	index, err := jobs.Read(filepath.Join(state, "async_jobs", id+".json"))
	if err != nil {
		return err
	}
	var session string
	index.Get("session_id", &session)
	event := map[string]any{"hook_event_name": "PostToolUse", "session_id": session, "tool_use_id": "maintenance-result-" + id,
		"tool_name": "mcp__codex-main__codex_result", "tool_input": map[string]any{"jobId": id}, "tool_response": map[string]any{"structuredContent": result}}
	_, err = maintenanceHook(ctx, state, event)
	return err
}

func maintenanceHook(ctx context.Context, state string, event map[string]any) (map[string]any, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(state, "maintenance", "jobs")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	input, err := os.CreateTemp(dir, ".finalize-input-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(input.Name())
	defer input.Close()
	if _, err = input.Write(data); err != nil {
		return nil, err
	}
	if _, err = input.Seek(0, 0); err != nil {
		return nil, err
	}
	output, err := os.CreateTemp(dir, ".finalize-output-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(output.Name())
	defer output.Close()
	executable, err := pluginroot.RuntimeExecutable(state)
	if err != nil {
		return nil, err
	}
	supervised, err := proc.StartSupervised(ctx, executable, []string{"supervisor"}, proc.Launch{Args: []string{executable, "enforce"},
		Env: append(os.Environ(), "PURSER_STATE_DIR="+state), Dir: dir, HideWindow: true, Stdin: input, Stdout: output})
	if err != nil {
		return nil, err
	}
	<-supervised.Done()
	finished := supervised.Result()
	if !finished.Confirmed {
		return nil, errors.Join(maintenance.ErrTestPatchTerminationUnconfirmed, finished.Err)
	}
	if finished.RootExit != 0 || ctx.Err() != nil {
		return nil, errors.Join(errors.New("maintenance enforcement failed or cancelled"), finished.Err, ctx.Err())
	}
	if _, err = output.Seek(0, 0); err != nil {
		return nil, err
	}
	var response map[string]any
	if err = json.NewDecoder(output).Decode(&response); err != nil {
		return nil, err
	}
	return response, nil
}

func maintenanceMain(args []string) int {
	if len(args) == 0 {
		return maintenanceUsage("")
	}
	command := args[0]
	args = args[1:]
	repo, report, decision := "", "", ""
	seenReport := false
	var acted []string
	reportType := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo", "--report", "--acted":
			if i+1 >= len(args) {
				return maintenanceUsage("argument " + args[i] + ": expected one argument")
			}
			i++
			switch args[i-1] {
			case "--repo":
				repo = args[i]
			case "--report":
				report = args[i]
				seenReport = true
			case "--acted":
				acted = append(acted, args[i])
			}
		default:
			if command == "consent" && decision == "" {
				decision = args[i]
			} else if command == "run" && reportType == "" {
				reportType = args[i]
			} else {
				return maintenanceUsage("unrecognized arguments: " + args[i])
			}
		}
	}
	if repo == "" {
		return maintenanceUsage("the following arguments are required: --repo")
	}
	if command == "skip-second-show" || command == "mark-done" {
		if !seenReport {
			return maintenanceUsage("the following arguments are required: --report")
		}
	} else if seenReport {
		return maintenanceUsage("unrecognized arguments: --report")
	}
	if command != "mark-done" && len(acted) > 0 {
		return maintenanceUsage("unrecognized arguments: --acted")
	}
	if command == "consent" && decision == "" {
		return maintenanceUsage("the following arguments are required: decision")
	}
	if command == "run" && reportType == "" {
		return maintenanceUsage("the following arguments are required: report_type")
	}
	if command == "consent" && decision != "yes" && decision != "no" {
		return maintenanceUsage("argument decision: invalid choice: " + strconv.Quote(decision) + " (choose from 'yes', 'no')")
	}
	if command == "run" && reportType != "cleanup" && reportType != "test-gaps" {
		return maintenanceUsage("argument report_type: invalid choice: " + strconv.Quote(reportType) + " (choose from 'cleanup', 'test-gaps')")
	}
	state := stateRoot()
	if command == "run" {
		root, err := maintenance.CanonicalRepository(repo)
		if err == nil {
			err = maintenance.RequireOptIn(state, root)
		}
		if err == nil {
			var path string
			path, err = maintenance.RunAndPersistReport(maintenance.Runner{Jobs: maintenanceJobs{}}, state, root, reportType)
			if err == nil && path != "" {
				if _, err = fmt.Fprintln(os.Stdout, path); err != nil {
					return 1
				}
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "maintenance report failed: "+err.Error())
			return 1
		}
		return 0
	}
	if command == "check" {
		root, err := maintenance.CanonicalRepository(repo)
		if err == nil {
			_, err = maintenance.DelayedCheck(root, state, 5*time.Minute, maintenance.Runner{Jobs: maintenanceJobs{}})
		}
		if errors.Is(err, os.ErrExist) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "maintenance report failed: "+err.Error())
			return 1
		}
		return 0
	}
	if command != "opt-in" && command != "opt-out" && command != "consent" && command != "skip-second-show" && command != "mark-done" {
		return maintenanceUsage("invalid choice: " + strconv.Quote(command) + " (choose from 'opt-in', 'opt-out', 'consent', 'skip-second-show', 'mark-done', 'run', 'check')")
	}
	path, err := maintenance.Control(state, command, repo, decision, report, acted)
	if err != nil {
		fmt.Fprintln(os.Stderr, "maintenance report failed: "+err.Error())
		return 1
	}
	if path != "" {
		if _, err := fmt.Fprintln(os.Stdout, path); err != nil {
			return 1
		}
	}
	return 0
}

func maintenanceUsage(message string) int {
	if message != "" {
		fmt.Fprintf(os.Stderr, "usage: maintenance %s\nmaintenance: error: %s\n", strings.Join(os.Args[2:], " "), message)
	} else {
		fmt.Fprintln(os.Stderr, "usage: maintenance {opt-in,opt-out,consent,skip-second-show,mark-done,run,check} ...")
	}
	return 2
}
