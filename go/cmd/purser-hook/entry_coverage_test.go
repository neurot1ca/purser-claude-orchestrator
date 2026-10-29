package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateEntry(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PURSER_STATE_DIR", root)
	t.Setenv("PURSER_TEST_DISABLE_CHILD_LAUNCH", "1")
	oldMaintenance, oldPrivate := startSessionMaintenance, launchPrivateChild
	startSessionMaintenance = func([]string, []string, string) error {
		t.Error("unexpected maintenance child launch")
		return errors.New("entry test forbids child launches")
	}
	launchPrivateChild = func([]string, []string, string) (int, int64, error) {
		t.Error("unexpected private child launch")
		return 0, 0, errors.New("entry test forbids child launches")
	}
	t.Cleanup(func() {
		startSessionMaintenance, launchPrivateChild = oldMaintenance, oldPrivate
		entries, err := os.ReadDir(filepath.Join(root, "codex_exec_mcp", "jobs"))
		if err != nil && !os.IsNotExist(err) || len(entries) != 0 {
			t.Errorf("unexpected job records: entries=%v err=%v", entries, err)
		}
		if _, err := os.Lstat(filepath.Join(root, "wt")); !os.IsNotExist(err) {
			t.Errorf("worktrees directory must be absent: %v", err)
		}
	})
	return root
}

func captureEntry(t *testing.T, call func() int) (int, string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	code := call()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = outW.Close()
	_ = errW.Close()
	out, outReadErr := io.ReadAll(outR)
	errout, errReadErr := io.ReadAll(errR)
	_ = outR.Close()
	_ = errR.Close()
	if outReadErr != nil || errReadErr != nil {
		t.Fatalf("capture output: stdout=%v stderr=%v", outReadErr, errReadErr)
	}
	return code, string(out), string(errout)
}

func entryStdin(t *testing.T, input string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestEntryMaintenanceArgumentErrors(t *testing.T) {
	root := isolateEntry(t)
	oldArgs := os.Args
	os.Args = []string{"purser-hook", "maintenance", "consent"}
	t.Cleanup(func() { os.Args = oldArgs })
	code, _, stderr := captureEntry(t, func() int { return maintenanceMain(nil) })
	if code != 2 || !strings.Contains(stderr, "usage: maintenance") {
		t.Fatalf("maintenance empty args = %d, stderr %q", code, stderr)
	}
	code, _, stderr = captureEntry(t, func() int { return maintenanceMain([]string{"unknown"}) })
	if code != 2 || !strings.Contains(stderr, "--repo") {
		t.Fatalf("maintenance missing repo = %d, stderr %q", code, stderr)
	}
	code, _, stderr = captureEntry(t, func() int { return maintenanceUsage("bad input") })
	if code != 2 || !strings.Contains(stderr, "maintenance: error: bad input") {
		t.Fatalf("maintenance usage = %d, stderr %q", code, stderr)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"opt-in", "--repo"}, "expected one argument"},
		{[]string{"skip-second-show", "--repo", root}, "required: --report"},
		{[]string{"consent", "--repo", root, "maybe"}, "argument decision: invalid choice"},
		{[]string{"run", "--repo", root, "unknown"}, "argument report_type: invalid choice"},
	} {
		code, out, stderr := captureEntry(t, func() int { return maintenanceMain(tc.args) })
		if code != 2 || out != "" || !strings.Contains(stderr, tc.want) {
			t.Fatalf("maintenance %v = %d, stdout %q, stderr %q", tc.args, code, out, stderr)
		}
	}
}

func TestEntryPrivateArgumentAndInputErrors(t *testing.T) {
	root := isolateEntry(t)
	if got := (operationInputError{"bad operation"}).Error(); got != "bad operation" {
		t.Fatalf("operation error = %q", got)
	}
	oldIn := os.Stdin
	t.Cleanup(func() { os.Stdin = oldIn })
	for _, input := range []string{"{", `{"args":{}} trailing`, `{}`, `{"args":null}`} {
		os.Stdin = entryStdin(t, input)
		if _, err := readOperationArgs(); err == nil {
			t.Fatalf("readOperationArgs(%q) succeeded", input)
		}
	}
	os.Stdin = entryStdin(t, `{"args":{"jobId":"x"}}`)
	if args, err := readOperationArgs(); err != nil || args["jobId"] != "x" {
		t.Fatalf("readOperationArgs valid = %#v, %v", args, err)
	}
	code, out, _ := captureEntry(t, func() int { return validateApplyMain(nil) })
	if code != 2 || !strings.Contains(out, "usage: purser-hook validate-apply") {
		t.Fatalf("validate-apply args = %d, %q", code, out)
	}
	code, out, _ = captureEntry(t, func() int { return retireWorktreeMain([]string{root, "job", "0"}) })
	if code != 1 || !strings.Contains(out, "generation must be positive") {
		t.Fatalf("retire-worktree generation = %d, %q", code, out)
	}
	code, out, _ = captureEntry(t, func() int { return operationMain(nil) })
	if code != 2 || !strings.Contains(out, "usage: purser-hook op") {
		t.Fatalf("operation args = %d, %q", code, out)
	}
	for _, tc := range []struct {
		operation, input, want string
	}{
		{"codex_result", "{", "malformed operation JSON"},
		{"unknown", `{"args":{}}`, "unknown operation"},
		{"codex_result", `{"args":{}}`, "jobId must be a nonempty string"},
	} {
		os.Stdin = entryStdin(t, tc.input)
		code, out, stderr := captureEntry(t, func() int { return operationMain([]string{tc.operation}) })
		if code != 2 || stderr != "" || !strings.Contains(out, tc.want) {
			t.Fatalf("operation %q = %d, stdout %q, stderr %q", tc.operation, code, out, stderr)
		}
	}
	oldArgs := os.Args
	os.Args = []string{"purser-hook", "op"}
	t.Cleanup(func() { os.Args = oldArgs })
	code, out, _ = captureEntry(t, func() int { return privateMain() })
	if code != 2 || !strings.Contains(out, "usage: purser-hook op") {
		t.Fatalf("private usage = %d, %q", code, out)
	}
}

func TestEntryWaitValidationMissingJob(t *testing.T) {
	root := isolateEntry(t)
	code, out, _ := captureEntry(t, func() int { return waitValidationMain(nil) })
	if code != 2 || !strings.Contains(out, "INVALID_ARGUMENT") {
		t.Fatalf("wait-validation args = %d, %q", code, out)
	}
	code, out, _ = captureEntry(t, func() int {
		return waitValidationMain([]string{root, "repository", "missing-job"})
	})
	if code == 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("missing validation job = %d, %q", code, out)
	}
}

func TestEntryRecoverStateErrors(t *testing.T) {
	root := isolateEntry(t)
	code, out, _ := captureEntry(t, func() int { return recoverStateMain([]string{"--unknown"}) })
	if code != 2 || strings.TrimSpace(out) != "" { // flag package writes usage to stderr.
		t.Fatalf("recover-state flag error = %d, stdout %q", code, out)
	}
	code, out, _ = captureEntry(t, func() int {
		return recoverStateMain([]string{"--state-root", filepath.Join(root, "absent")})
	})
	if code != 1 || !strings.Contains(out, "state root must be an existing directory") {
		t.Fatalf("recover-state missing root = %d, %q", code, out)
	}
	code, out, _ = captureEntry(t, func() int { return recoverStateMain(nil) })
	if code != 0 || !strings.Contains(out, `"apply":false`) {
		t.Fatalf("recover-state empty root = %d, %q", code, out)
	}
}

func TestEntryOptimizeArgumentErrors(t *testing.T) {
	isolateEntry(t)
	code, _, stderr := captureEntry(t, func() int { return optimizeMain([]string{"unexpected"}) })
	if code != 2 || !strings.Contains(stderr, "takes no arguments") {
		t.Fatalf("optimize args = %d, %q", code, stderr)
	}
	code, _, stderr = captureEntry(t, func() int { return optimizeMain([]string{"telemetry", "unexpected"}) })
	if code != 2 || !strings.Contains(stderr, "only --json") {
		t.Fatalf("optimize telemetry args = %d, %q", code, stderr)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	code, out, _ := captureEntry(t, func() int { return optimizeMain([]string{"telemetry", "--json"}) })
	if code != 0 || !strings.HasPrefix(out, "{") {
		t.Fatalf("optimize telemetry = %d, %q", code, out)
	}
}

func TestEntryMalformedAndErrorName(t *testing.T) {
	isolateEntry(t)
	oldOut, oldErr := os.Stdout, os.Stderr
	code, out, stderr := captureEntry(t, func() int { malformed("JSONDecodeError", true); return 0 })
	if code != 0 || out != "{}\n" || !strings.Contains(stderr, "JSONDecodeError: malformed hook input") {
		t.Fatalf("malformed = %d, stdout %q, stderr %q", code, out, stderr)
	}
	os.Stdout, os.Stderr = oldOut, oldErr
	if got := errorName(os.ErrNotExist); got != "RuntimeError: file does not exist" {
		t.Fatalf("errorName = %q", got)
	}
}
