package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/jobs"
	"purser-hook/internal/testutil"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "supervisor" {
		os.Exit(jobs.DurableSupervisorMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "enforce" {
		main()
		os.Exit(0)
	}
	requestCleanup = func(string) error { return nil }
	jobs.DisableApplyValidationForTests()
	_ = os.Setenv("PURSER_APPLY_VALIDATION_CHILD", "1")
	os.Exit(testutil.RunIsolated(m))
}

func TestMaintenanceHookRunsEnforcementUnderSupervision(t *testing.T) {
	state := synchronousScratchDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := maintenanceHook(ctx, state, map[string]any{"hook_event_name": "PreToolUse", "tool_name": "mcp__codex-main__codex_start", "tool_input": map[string]any{"prompt": "missing orchestration contract"}, "session_id": "maintenance-fixture", "tool_use_id": "fixture", "cwd": state})
	if err != nil {
		t.Fatal(err)
	}
	hook, _ := response["hookSpecificOutput"].(map[string]any)
	if hook["permissionDecision"] != "deny" {
		t.Fatalf("enforcement was bypassed: %#v", response)
	}
}

func TestMaintenanceAdmissionCancelsJobAppearingAfterDispatchError(t *testing.T) {
	state, root := synchronousScratchDir(t), synchronousScratchDir(t)
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldHook, oldDispatch := maintenanceStartHook, maintenanceStartDispatch
	t.Cleanup(func() { maintenanceStartHook, maintenanceStartDispatch = oldHook, oldDispatch })
	call := "delayed-maintenance-job"
	sum := sha256.Sum256([]byte(call))
	id := hex.EncodeToString(sum[:])
	write := func(path string, value map[string]any) error {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0600)
	}
	maintenanceStartHook = func(ctx context.Context, state string, event map[string]any) (map[string]any, error) {
		keySum := sha256.Sum256([]byte("maintenance-" + contextio.ProjectKey(root) + "\x00fixture"))
		key := hex.EncodeToString(keySum[:])
		if err := write(filepath.Join(state, "pending", key+".json"), map[string]any{"async_call": call}); err != nil {
			return nil, err
		}
		return map[string]any{"hookSpecificOutput": map[string]any{"updatedInput": map[string]any{"worktree": true, "call": call}}}, nil
	}
	dispatchErr := errors.New("dispatch failed before job became visible")
	appeared := make(chan error, 1)
	path := filepath.Join(state, "codex_exec_mcp", "jobs", id+".json")
	maintenanceStartDispatch = func(context.Context, string, map[string]any, string) (map[string]any, error) {
		go func() {
			time.Sleep(150 * time.Millisecond)
			appeared <- write(path, map[string]any{"jobId": id, "state": "QUEUED", "leaseQueued": true, "repoRoot": root})
		}()
		return nil, dispatchErr
	}
	_, err := (maintenanceJobs{}).Start(context.Background(), state, map[string]any{
		"worktree": true, "cwd": root, "call": "fixture", "prompt": "write tests",
		"maintenanceDeadlineAt": time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339Nano),
	})
	if !errors.Is(err, dispatchErr) {
		t.Fatalf("dispatch error lost: %v", err)
	}
	select {
	case err := <-appeared:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch did not create the delayed job")
	}
	job, err := jobs.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	job.Get("state", &status)
	if status != "CANCELLED" {
		t.Fatalf("late job remains %s; Start returned %v", status, err)
	}
}

func TestCleanupLaunchRefusesTestBinary(t *testing.T) {
	oldLaunch := launchPrivateChild
	launches := 0
	launchPrivateChild = func([]string, []string, string) (int, int64, error) {
		launches++
		return 42, 7, nil
	}
	t.Cleanup(func() { launchPrivateChild = oldLaunch })
	root := t.TempDir()
	pid, identity, err := launchPrivate(root, "cleanup", root)
	if err == nil || !strings.Contains(err.Error(), "test binary") || pid != 0 || identity != 0 || launches != 0 {
		t.Fatalf("cleanup launch: pid=%d identity=%d err=%v launches=%d", pid, identity, err, launches)
	}
	allowCleanupTestChild = true
	t.Cleanup(func() { allowCleanupTestChild = false })
	pid, identity, err = launchPrivate(root, "cleanup", root)
	if err != nil || pid != 42 || identity != 7 || launches != 1 {
		t.Fatalf("opted-in cleanup launch: pid=%d identity=%d err=%v launches=%d", pid, identity, err, launches)
	}
}
