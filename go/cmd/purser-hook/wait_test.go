package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/testutil"
	"purser-hook/internal/waiter"
)

func TestWaitReobserveProcess(t *testing.T) {
	root := os.Getenv("PURSER_WAIT_REOBSERVE_ROOT")
	if root == "" {
		return
	}
	if os.Getenv("PURSER_WAIT_EMPTY_EXIT") == "1" {
		os.Exit(255)
	}
	t.Setenv("PURSER_STATE_DIR", root)
	t.Setenv("PURSER_WAIT_MAX_SECONDS", "1")
	os.Exit(waitMain([]string{"samejob"}))
}

func TestParseWaitArgs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		id, owner string
		seconds   int64
		ok        bool
	}{
		{name: "plain", args: []string{"job"}, id: "job", ok: true},
		{name: "owner then bound", args: []string{"job", "--session-owner", "owner", "--max-seconds", "150"}, id: "job", owner: "owner", seconds: 150, ok: true},
		{name: "bound then owner", args: []string{"job", "--max-seconds", "150", "--session-owner", "owner"}, id: "job", owner: "owner", seconds: 150, ok: true},
		{name: "invalid bound", args: []string{"job", "--max-seconds", "nope"}, id: "job", ok: false},
		{name: "zero bound", args: []string{"job", "--max-seconds", "0"}, id: "job", ok: false},
		{name: "duplicate bound", args: []string{"job", "--max-seconds", "1", "--max-seconds", "2"}, id: "job", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, owner, seconds, ok := parseWaitArgs(tc.args)
			if id != tc.id || owner != tc.owner || seconds != tc.seconds || ok != tc.ok {
				t.Fatalf("parseWaitArgs(%v) = %q, %q, %d, %t", tc.args, id, owner, seconds, ok)
			}
		})
	}
}

func TestWaitSessionOwnerProcess(t *testing.T) {
	if root := os.Getenv("PURSER_WAIT_OWNER_ROOT"); root != "" {
		t.Setenv("PURSER_STATE_DIR", root)
		os.Exit(waitMain([]string{"missing", "--session-owner", os.Getenv("PURSER_SESSION_OWNER")}))
	}
}

func TestWaitSessionOwnerDeadReusedAndLive(t *testing.T) {
	scratch, err := filepath.Abs("../../../.tmp")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "wait-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	current, err := waiter.PinOwner(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		owner waiter.Owner
		want  string
	}{
		{name: "dead", owner: waiter.Owner{PID: 1 << 30, Created: 1}, want: "SESSION_OWNER_DEAD"},
		{name: "pid reused", owner: waiter.Owner{PID: current.PID, Created: current.Created + 1}, want: "SESSION_OWNER_STALE"},
		{name: "live matching owner", owner: current},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := testutil.Hide(exec.Command(os.Args[0], "-test.run=^TestWaitSessionOwnerProcess$"))
			command.Env = append(os.Environ(), "PURSER_WAIT_OWNER_ROOT="+root, "PURSER_SESSION_OWNER="+tc.owner.String())
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("wait exit = %v, stdout %q, stderr %q; want exit 2", err, stdout.String(), stderr.String())
			}
			if tc.want == "" {
				if stderr.Len() != 0 || strings.TrimSpace(stdout.String()) != "purser-wait missing UNKNOWN_JOB" {
					t.Fatalf("live owner did not reach job wait: stdout %q, stderr %q", stdout.String(), stderr.String())
				}
				return
			}
			if stdout.Len() != 0 || strings.Count(stderr.String(), "\n") != 1 ||
				!strings.Contains(stderr.String(), tc.want) || !strings.Contains(stderr.String(), "retry: purser-hook wait missing") {
				t.Fatalf("wait rejection: stdout %q, stderr %q; want one stderr line with %s and retry command", stdout.String(), stderr.String(), tc.want)
			}
			if tc.want == "SESSION_OWNER_DEAD" && strings.Contains(stderr.String(), "--session-owner") {
				t.Fatalf("dead owner retry retained the dead token: %q", stderr.String())
			}
		})
	}
}

func TestWaitEmpty255ReobservesSameJob(t *testing.T) {
	scratch, err := filepath.Abs("../../../.tmp")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "wait-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	dir := filepath.Join(root, "codex_exec_mcp", "jobs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "samejob.json")
	job := map[string]any{"jobId": "samejob", "state": "RUNNING", "generation": 7,
		"ownershipToken": "owner", "signalToken": "signal", "deadlineAt": float64(time.Now().Add(time.Minute).UnixNano()) / 1e9}
	write := func() {
		data, _ := json.Marshal(job)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		empty bool
		state string
		code  int
		line  string
	}{
		{empty: true, state: "RUNNING", code: 255},
		{state: "RUNNING", code: 0, line: "purser-wait samejob STILL_WAITING"},
		{state: "SUCCEEDED", code: 0, line: "purser-wait samejob SUCCEEDED"},
	} {
		job["state"] = tc.state
		write()
		command := testutil.Hide(exec.Command(executable, "-test.run=^TestWaitReobserveProcess$"))
		command.Env = append(os.Environ(), "PURSER_WAIT_REOBSERVE_ROOT="+root, "PURSER_WAIT_EMPTY_EXIT=0")
		if tc.empty {
			command.Env[len(command.Env)-1] = "PURSER_WAIT_EMPTY_EXIT=1"
		}
		output, err := command.CombinedOutput()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		if code != tc.code || strings.TrimSpace(string(output)) != tc.line {
			t.Fatalf("wait = %d/%q, want %d/%q", code, output, tc.code, tc.line)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var current map[string]any
		if err := json.Unmarshal(data, &current); err != nil {
			t.Fatal(err)
		}
		if current["state"] != tc.state || current["generation"] != float64(7) || current["ownershipToken"] != "owner" || current["capacityRetryAttempts"] != nil {
			t.Fatalf("waiter changed worker identity: %#v", current)
		}
	}
}
