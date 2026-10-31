package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/contextio"
)

func TestMaintenanceValidationDisablesDownloads(t *testing.T) {
	env := moduleValidationEnvironment(t.TempDir(), t.TempDir(), true)
	for _, want := range []string{"GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off"} {
		found := false
		for _, value := range env {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("maintenance validation environment lacks %s", want)
		}
	}
}

func TestWaitApplyValidationReportsTerminalClasses(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason, failing string
	}{
		{name: "passed", status: "passed"},
		{name: "failed", status: "failed", reason: "go test failed", failing: "TestBroken"},
		{name: "stale", status: "stale", reason: "checkout changed"},
		{name: "unknown", status: "future_status", reason: "future"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			id, key := strings.Repeat("a", 64), strings.Repeat("b", 64)
			path := validationRecordPath(root, key, id)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			record := applyValidation{JobID: id, Status: tc.status, Reason: tc.reason, FailedTests: []string{}}
			if tc.failing != "" {
				record.FailedTests = []string{tc.failing}
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			got := WaitApplyValidation(root, key, id)
			if got.ExitCode != 0 || len(got.Lines) != 1 || !strings.Contains(got.Lines[0], `"status":"`+tc.status+`"`) || !strings.Contains(got.Lines[0], tc.reason) && tc.reason != "" || !strings.Contains(got.Lines[0], tc.failing) && tc.failing != "" {
				t.Fatalf("WaitApplyValidation = %#v", got)
			}
		})
	}
}

func TestWaitApplyValidationRunningReturnsStillWaiting(t *testing.T) {
	t.Setenv("PURSER_WAIT_MAX_SECONDS", "1")
	root := t.TempDir()
	id, key := strings.Repeat("a", 64), strings.Repeat("b", 64)
	path := validationRecordPath(root, key, id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(applyValidation{JobID: id, Status: "running"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got := WaitApplyValidation(root, key, id)
	if got.ExitCode != 5 || len(got.Lines) != 1 || got.Lines[0] != "purser-validation STILL_WAITING" {
		t.Fatalf("WaitApplyValidation = %#v", got)
	}
}

func TestWaitApplyValidationRetriesTransientRecordRead(t *testing.T) {
	root := t.TempDir()
	id, key := strings.Repeat("a", 64), strings.Repeat("b", 64)
	path := validationRecordPath(root, key, id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"partial"`), 0600); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(40 * time.Millisecond)
		data, _ := json.Marshal(applyValidation{JobID: id, Status: "passed"})
		_ = os.WriteFile(path, data, 0600)
	}()
	got := WaitApplyValidation(root, key, id)
	if got.ExitCode != 0 || len(got.Lines) != 1 || !strings.Contains(got.Lines[0], `"status":"passed"`) {
		t.Fatalf("WaitApplyValidation = %#v", got)
	}
}

func validationFixture(t *testing.T, paths []string) (string, string, applyValidation) {
	t.Helper()
	base := jobsScratchDir(t, "apply-validation-")
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	write(t, filepath.Join(repo, "go.mod"), "module example.test/validation\n\ngo 1.23\n")
	write(t, filepath.Join(repo, "main.go"), "package validation\nfunc Value() int { return 1 }\n")
	git(t, repo, "add", "--all")
	git(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-qm", "base")
	write(t, filepath.Join(repo, "main.go"), "package validation\nfunc Value() int { return 2 }\n")
	revision, snapshot, err := checkoutRevision(repo)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "state"), repo, applyValidation{JobID: strings.Repeat("a", 64), Repository: repo, Revision: revision, Snapshot: snapshot, Paths: paths, Status: "pending"}
}

func runValidationFixture(t *testing.T, status string, commandErr error) applyValidation {
	t.Helper()
	root, _, record := validationFixture(t, []string{"main.go"})
	key := validationRepositoryKey(record.Repository)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	prior := runApplyValidationCommand
	runApplyValidationCommand = func(_ string, _ []string, args []string, _ time.Duration, _ io.Writer, result *applyValidation) error {
		if len(args) > 0 && args[0] == "test" && result != nil {
			if commandErr != nil {
				result.Counts = map[string]int{"tests_failed": 1}
				result.FailedTests = []string{"TestBad"}
			} else {
				result.Counts = map[string]int{"tests_passed": 1}
				result.Reason = "passed; leftover child process held output"
			}
			return commandErr
		}
		if errors.Is(commandErr, context.DeadlineExceeded) {
			return commandErr
		}
		return nil
	}
	t.Cleanup(func() { runApplyValidationCommand = prior })
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	got, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != status {
		t.Fatalf("status=%q want %q (%s)", got.Status, status, got.Reason)
	}
	return got
}

func TestValidationPass(t *testing.T) {
	got := runValidationFixture(t, "passed", nil)
	if got.Counts["tests_passed"] != 1 {
		t.Fatalf("counts missing: %#v", got.Counts)
	}
	if got.Reason != "passed; leftover child process held output" {
		t.Fatalf("pass reason not retained: %q", got.Reason)
	}
}

func TestApplyValidationLintSummary(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
		err                error
		available          bool
	}{
		{name: "missing"},
		{name: "issues", available: true, output: `{"Issues":[{"FromLinter":"unused"},{"FromLinter":"errcheck"},{"FromLinter":"errcheck"}]}`, want: "3 new issues: errcheck 2, unused 1"},
		{name: "error", available: true, err: errors.New("lint failed"), want: "skipped: error"},
		{name: "timeout", available: true, err: context.DeadlineExceeded, want: "skipped: timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, repo, record := validationFixture(t, []string{"main.go"})
			key := validationRepositoryKey(repo)
			path := validationRecordPath(root, key, record.JobID)
			if err := writeApplyValidation(path, record); err != nil {
				t.Fatal(err)
			}
			lintCalls := 0
			priorRunner, priorLookPath := runApplyValidationCommand, validationLookPath
			runApplyValidationCommand = func(_ string, _ []string, args []string, _ time.Duration, output io.Writer, result *applyValidation) error {
				if len(args) > 0 && strings.Contains(args[0], "golangci-lint") {
					lintCalls++
					_, _ = io.WriteString(output, tc.output)
					return tc.err
				}
				if result != nil {
					result.Counts = map[string]int{"tests_passed": 1}
				}
				return nil
			}
			validationLookPath = func(name string) (string, error) {
				if strings.Contains(name, "golangci-lint") && tc.available {
					return name, nil
				}
				return "", exec.ErrNotFound
			}
			t.Cleanup(func() { runApplyValidationCommand, validationLookPath = priorRunner, priorLookPath })
			if err := RunApplyValidation(root, key, record.JobID); err != nil {
				t.Fatal(err)
			}
			got, err := readApplyValidation(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "passed" {
				t.Fatalf("status=%q; lint must not gate validation", got.Status)
			}
			if tc.want != "" && got.Lint != tc.want {
				t.Fatalf("lint=%q want %q", got.Lint, tc.want)
			}
			if tc.want == "" && got.Lint != "" {
				t.Fatalf("unexpected lint field %q", got.Lint)
			}
			if tc.available != (lintCalls == 1) {
				t.Fatalf("lint calls=%d available=%v", lintCalls, tc.available)
			}
			if tc.want != "" && validationSummary(got)["lint"] != tc.want {
				t.Fatalf("summary lint=%v want %q", validationSummary(got)["lint"], tc.want)
			}
			if tc.want == "" {
				if _, exists := validationSummary(got)["lint"]; exists {
					t.Fatal("summary unexpectedly contains lint")
				}
			}
		})
	}
}

func TestApplyValidationLintTimeoutWithIssues(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	priorRunner, priorLookPath := runApplyValidationCommand, validationLookPath
	t.Cleanup(func() { runApplyValidationCommand, validationLookPath = priorRunner, priorLookPath })
	validationLookPath = func(name string) (string, error) { return name, nil }
	runApplyValidationCommand = func(_ string, _ []string, args []string, _ time.Duration, output io.Writer, result *applyValidation) error {
		if strings.Contains(args[0], "golangci-lint") {
			_, _ = io.WriteString(output, `{"Issues":[{"FromLinter":"errcheck"}]}`)
			return context.DeadlineExceeded
		}
		if result != nil {
			result.Counts = map[string]int{"tests_failed": 1}
			result.FailedTests = []string{"TestExistingFailure"}
			return errors.New("exit status 1")
		}
		return nil
	}
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	got, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	summary := validationSummary(got)
	if summary["lint"] != "skipped: timeout" {
		t.Fatalf("lint=%v want skipped: timeout", summary["lint"])
	}
	if summary["status"] != "failed" {
		t.Fatalf("lint changed status: %v", summary["status"])
	}
	if counts := summary["counts"].(map[string]int); len(counts) != 1 || counts["tests_failed"] != 1 {
		t.Fatalf("lint changed counts: %v", counts)
	}
	if failures := summary["failing_tests"].([]string); !slices.Equal(failures, []string{"TestExistingFailure"}) {
		t.Fatalf("lint changed failing_tests: %v", failures)
	}
}

func TestValidationRetriesLockedIndex(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(repo, ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lockPath) })
	prior := runApplyValidationCommand
	runApplyValidationCommand = func(string, []string, []string, time.Duration, io.Writer, *applyValidation) error { return nil }
	t.Cleanup(func() { runApplyValidationCommand = prior })
	go func() {
		time.Sleep(90 * time.Millisecond)
		_ = os.Remove(lockPath)
	}()
	started := time.Now()
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("validation did not wait for index lock: %s", elapsed)
	}
	got, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" {
		t.Fatalf("validation status=%q reason=%q", got.Status, got.Reason)
	}
}

func TestValidationGitFailureIncludesCommandAndStderr(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/missing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	got, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "launch_error" || !strings.Contains(got.Reason, "git rev-parse HEAD failed:") || !strings.Contains(got.Reason, "fatal:") {
		t.Fatalf("validation status=%q reason=%q", got.Status, got.Reason)
	}
}

func TestPluginValidationRunsOnlyTrackedTestFiles(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"hooks/mod/changed.ts"})
	write(t, filepath.Join(repo, ".gitignore"), "ignored/\n")
	write(t, filepath.Join(repo, "hooks", "mod", "one.test.ts"), "")
	write(t, filepath.Join(repo, "hooks", "mod", "two.test.tsx"), "")
	write(t, filepath.Join(repo, "ignored", "one.test.ts"), "")
	write(t, filepath.Join(repo, "ignored", "two.test.tsx"), "")
	git(t, repo, "add", ".gitignore", "hooks/mod/one.test.ts", "hooks/mod/two.test.tsx")
	var err error
	record.Revision, record.Snapshot, err = checkoutRevision(repo)
	if err != nil {
		t.Fatal(err)
	}
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	var gotFiles []string
	priorRunner, priorLookPath := runApplyValidationCommand, validationLookPath
	runApplyValidationCommand = func(_ string, _ []string, args []string, _ time.Duration, _ io.Writer, _ *applyValidation) error {
		if len(args) >= 6 && args[1] == "plugin" && args[2] == "test" && args[3] == "--file" {
			gotFiles = append(gotFiles, args[4])
		}
		return nil
	}
	validationLookPath = func(tool string) (string, error) {
		if tool == "claude" {
			return "claude", nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { runApplyValidationCommand, validationLookPath = priorRunner, priorLookPath })
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	want := []string{"hooks/mod/one.test.ts", "hooks/mod/two.test.tsx"}
	if strings.Join(gotFiles, "\n") != strings.Join(want, "\n") {
		t.Fatalf("plugin test files=%v want tracked files %v", gotFiles, want)
	}
	result, err := readApplyValidation(path)
	if err != nil || result.PluginStatus != "passed" {
		t.Fatalf("plugin validation result=%+v err=%v", result, err)
	}
}

func TestValidationPackageSelectionIncludesDeletedGoAndTouchedModule(t *testing.T) {
	t.Setenv("GOWORK", "off")
	root, repo, _ := validationFixture(t, []string{"main.go"})
	if err := os.Remove(filepath.Join(repo, "main.go")); err != nil {
		t.Fatal(err)
	}
	_, modules, packages, err := selectValidationPackages(root, repo, []string{"main.go"})
	if err != nil || len(modules) != 1 || len(packages) != 1 || packages[0] != repo+"|." {
		t.Fatalf("deleted Go path selection: modules=%v packages=%v err=%v", modules, packages, err)
	}
	write(t, filepath.Join(repo, "main.go"), "package validation\nfunc Value() int { return 3 }\n")
	_, modules, packages, err = selectValidationPackages(root, repo, []string{"go.mod"})
	if err != nil || len(modules) != 1 || len(packages) == 0 {
		t.Fatalf("module configuration selection: modules=%v packages=%v err=%v", modules, packages, err)
	}
}

func TestValidationPackageSelectionSkipsIgnoredGoDirectories(t *testing.T) {
	root, repo, _ := validationFixture(t, []string{"pkg/testdata/bad/go.mod", "pkg/testdata/bad/bad.go"})
	_, modules, packages, err := selectValidationPackages(root, repo, []string{
		"pkg/testdata/bad/go.mod", "pkg/testdata/bad/bad.go",
	})
	if err != nil || len(modules) != 0 || len(packages) != 0 {
		t.Fatalf("ignored testdata paths selected validation: modules=%v packages=%v err=%v", modules, packages, err)
	}
}

func TestValidationPackageSelectionDropsMissingDirectories(t *testing.T) {
	root, repo, _ := validationFixture(t, []string{"main.go"})
	_, modules, packages, err := selectValidationPackages(root, repo, []string{
		"main.go", filepath.Join("removed", "go.mod"), filepath.Join("removed", "pkg", "old.go"),
	})
	if err != nil || len(modules) != 1 || modules[0] != repo || len(packages) != 1 || packages[0] != repo+"|." {
		t.Fatalf("missing path selection: modules=%v packages=%v err=%v", modules, packages, err)
	}
}

func TestValidationPythonManifestDirectory(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"pkg/tests/test_one.py"})
	write(t, filepath.Join(repo, "pkg", "pyproject.toml"), "[tool.pytest.ini_options]\n")
	write(t, filepath.Join(repo, "pkg", "tests", "test_one.py"), "")
	commands := selectEcosystemValidationCommands(repo, record.Paths)
	if len(commands) != 1 || commands[0].tool != "python" || commands[0].dir != filepath.Join(repo, "pkg") || strings.Join(commands[0].args, " ") != "-m pytest -q" {
		t.Fatalf("Python validation selection=%+v (root %s)", commands, root)
	}
}

func TestValidationNpmPlaceholderIsNotRun(t *testing.T) {
	_, repo, record := validationFixture(t, []string{"web/test.ts"})
	write(t, filepath.Join(repo, "web", "package.json"), `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`)
	write(t, filepath.Join(repo, "web", "test.ts"), "")
	if got := selectEcosystemValidationCommands(repo, record.Paths); len(got) != 0 {
		t.Fatalf("placeholder npm test was selected: %+v", got)
	}
}

func TestValidationProfileCommandOverridesEcosystemDetection(t *testing.T) {
	got := runEcosystemValidationFixture(t, []string{"test.py"}, func(repo string) {
		write(t, filepath.Join(repo, "pyproject.toml"), "")
		write(t, filepath.Join(repo, "test.py"), "")
		write(t, filepath.Join(repo, ".orchestrator", "profile.toml"), "schema_version = 1\n[validation]\ncommand = \"custom-check --all\"\n")
	}, func(tool string) (string, error) { return "/fake/" + tool, nil }, func(dir string, _ []string, args []string, _ time.Duration, _ io.Writer, _ *applyValidation) error {
		if dir == "" || len(args) != 3 || args[len(args)-1] != "custom-check --all" {
			t.Fatalf("profile command invocation dir=%q args=%v", dir, args)
		}
		return nil
	})
	if got.Status != "passed" || len(got.Commands) != 1 || !strings.Contains(got.Commands[0], "custom-check --all") {
		t.Fatalf("profile command result=%+v", got)
	}
}

func TestValidationMissingEcosystemToolIsSkipped(t *testing.T) {
	got := runEcosystemValidationFixture(t, []string{"test.py"}, func(repo string) {
		write(t, filepath.Join(repo, "pyproject.toml"), "")
		write(t, filepath.Join(repo, "test.py"), "")
	}, func(string) (string, error) { return "", exec.ErrNotFound }, func(string, []string, []string, time.Duration, io.Writer, *applyValidation) error {
		t.Fatal("runner called for missing pytest executable")
		return nil
	})
	if got.Status != "skipped" || got.Reason != "python3 is unavailable" && got.Reason != "python is unavailable" {
		t.Fatalf("missing tool result=%+v", got)
	}
}

func TestValidationFailingEcosystemCommandFails(t *testing.T) {
	got := runEcosystemValidationFixture(t, []string{"test.py"}, func(repo string) {
		write(t, filepath.Join(repo, "pyproject.toml"), "")
		write(t, filepath.Join(repo, "test.py"), "")
	}, func(tool string) (string, error) { return "/fake/" + tool, nil }, func(string, []string, []string, time.Duration, io.Writer, *applyValidation) error {
		return errors.New("exit status 1")
	})
	if got.Status != "failed" || got.Reason != "python -m pytest -q failed" {
		t.Fatalf("failing ecosystem result=%+v", got)
	}
}

func runEcosystemValidationFixture(t *testing.T, paths []string, setup func(string), lookPath func(string) (string, error), runner func(string, []string, []string, time.Duration, io.Writer, *applyValidation) error) applyValidation {
	t.Helper()
	root, repo, record := validationFixture(t, paths)
	setup(repo)
	revision, snapshot, err := checkoutRevision(repo)
	if err != nil {
		t.Fatal(err)
	}
	record.Revision, record.Snapshot = revision, snapshot
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	priorRunner, priorLookPath := runApplyValidationCommand, validationLookPath
	runApplyValidationCommand, validationLookPath = runner, lookPath
	t.Cleanup(func() { runApplyValidationCommand, validationLookPath = priorRunner, priorLookPath })
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	result, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCheckoutRevisionFailsWhenDirectoryReplacesTrackedFile(t *testing.T) {
	_, repo, _ := validationFixture(t, []string{"main.go"})
	if err := os.Remove(filepath.Join(repo, "main.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "main.go"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := checkoutRevision(repo); err == nil {
		t.Fatal("directory replacing tracked file produced an unverifiable content tree")
	}
}

func TestCheckoutRevisionHandlesDeletedTrackedFile(t *testing.T) {
	_, repo, _ := validationFixture(t, []string{"main.go"})
	if err := os.Remove(filepath.Join(repo, "main.go")); err != nil {
		t.Fatal(err)
	}
	revision, snapshot, err := checkoutRevision(repo)
	if err != nil {
		t.Fatalf("checkoutRevision with deleted tracked file: %v", err)
	}
	if revision == "" || snapshot == "" {
		t.Fatalf("checkoutRevision returned empty revisions: revision=%q snapshot=%q", revision, snapshot)
	}
}

func TestCheckoutRevisionAllowsGitlinkDirectory(t *testing.T) {
	_, repo, record := validationFixture(t, []string{"main.go"})
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+record.Revision+",sub")
	if err := os.Mkdir(filepath.Join(repo, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := checkoutRevision(repo)
	if err != nil {
		t.Fatalf("checkoutRevision with gitlink directory: %v", err)
	}
	entry, err := runGit(repo, nil, "ls-tree", snapshot, "--", "sub")
	if err != nil || !strings.HasPrefix(string(entry), "160000 commit "+record.Revision+"\tsub") {
		t.Fatalf("gitlink missing from snapshot: entry=%q err=%v", entry, err)
	}
}

func TestValidationAllSkippedIsNotPassing(t *testing.T) {
	if got := mapValidationState("skipped", "no Go packages or plugin paths were changed"); got != "SKIPPED" {
		t.Fatalf("all-skipped validation state=%q, want SKIPPED", got)
	}
}

func TestValidationFailReturnsFailingTestNames(t *testing.T) {
	got := runValidationFixture(t, "failed", errors.New("test failure"))
	if len(got.FailedTests) != 1 || got.FailedTests[0] != "TestBad" {
		t.Fatalf("failing tests=%v", got.FailedTests)
	}
}

func TestValidationLaunchError(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	prior := launchApplyValidation
	launches := 0
	launchApplyValidation = func([]string, []string, string) (int, int64, error) {
		launches++
		return 0, 0, errors.New("spawn denied")
	}
	t.Cleanup(func() { launchApplyValidation = prior })
	value := map[string]any{"applied": true, "paths": record.Paths}
	enqueueAppliedValidation(root, map[string]any{"jobId": record.JobID, "repoRoot": repo}, value)
	got, err := readApplyValidation(validationRecordPath(root, validationRepositoryKey(repo), record.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "launch_error" || launches != 1 {
		t.Fatalf("record=%#v launches=%d", got, launches)
	}
}

func TestValidationDefaultHookRecordsWithoutLaunchingDetachedChild(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	launches := applyValidationTestLaunches.Load()
	value := map[string]any{"applied": true, "paths": record.Paths}
	enqueueAppliedValidation(root, map[string]any{"jobId": record.JobID, "repoRoot": repo}, value)
	if got := applyValidationTestLaunches.Load() - launches; got != 1 {
		t.Fatalf("default test hook calls=%d, want 1 recorded no-op", got)
	}
	if validation := value["validation"].(map[string]any); validation["status"] != "pending" {
		t.Fatalf("validation result=%#v", validation)
	}
}

func TestValidationPendingInstructionReferencesWaiter(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	value := map[string]any{"applied": true, "paths": record.Paths}
	enqueueAppliedValidation(root, map[string]any{"jobId": record.JobID, "repoRoot": repo}, value)
	command := stringValue(value["waiter"])
	next := stringValue(value["next"])
	if command == "" || strings.Contains(next, command) {
		t.Fatalf("pending validation instruction duplicated waiter command: waiter=%q next=%q", command, next)
	}
	want := "Run the waiter command in the background"
	if runtime.GOOS == "windows" {
		want += " (use the PowerShell tool on Windows)"
	}
	want += "; if it prints STILL_WAITING, run it again, otherwise use its final line to report host validation."
	if next != want {
		t.Fatalf("pending validation next=%q, want %q", next, want)
	}
}

func TestValidationRefusesGoTestExecutable(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	prior := applyValidationExecutable
	applyValidationExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "jobs.test.exe"), nil }
	t.Cleanup(func() { applyValidationExecutable = prior })
	launches := applyValidationTestLaunches.Load()
	value := map[string]any{"applied": true, "paths": record.Paths}
	enqueueAppliedValidation(root, map[string]any{"jobId": record.JobID, "repoRoot": repo}, value)
	got, err := readApplyValidation(validationRecordPath(root, validationRepositoryKey(repo), record.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "launch_error" || !strings.Contains(got.Reason, "test executable") || applyValidationTestLaunches.Load() != launches {
		t.Fatalf("record=%#v launches=%d", got, applyValidationTestLaunches.Load()-launches)
	}
}

func TestValidationChildEnvironmentMakesEnqueueNoop(t *testing.T) {
	t.Setenv(applyValidationChildEnv, "1")
	root, repo, record := validationFixture(t, []string{"main.go"})
	launches := applyValidationTestLaunches.Load()
	value := map[string]any{"applied": true, "paths": record.Paths}
	enqueueAppliedValidation(root, map[string]any{"jobId": record.JobID, "repoRoot": repo}, value)
	if applyValidationTestLaunches.Load() != launches {
		t.Fatalf("child environment attempted a detached launch")
	}
	if _, err := os.Stat(validationRecordPath(root, validationRepositoryKey(repo), record.JobID)); !os.IsNotExist(err) {
		t.Fatalf("child environment wrote a validation record: %v", err)
	}
}

func TestValidationEnqueueFailureReturnsResultWithoutDurableValidation(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "missing-repository")
	id := strings.Repeat("b", 64)
	jobDir := filepath.Join(root, "codex_exec_mcp", "jobs")
	if err := os.MkdirAll(jobDir, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(jobDir, id+".out"), "done")
	jobBytes, err := json.Marshal(map[string]any{"jobId": id, "state": "SUCCEEDED", "repoRoot": repo,
		"outputFile": id + ".out", "threadId": "thread", "finishedAt": float64(time.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(jobDir, id+".json"), jobBytes, 0600); err != nil {
		t.Fatal(err)
	}
	value := map[string]any{"applied": true, "paths": []string{"main.go"}}
	enqueueAppliedValidation(root, map[string]any{"jobId": id, "repoRoot": repo}, value)
	validation, ok := value["validation"].(map[string]any)
	if !ok || validation["status"] != "launch_error" {
		t.Fatalf("validation result missing from apply result: %#v", value)
	}
	result := resultJob(jobDir, id, float64(time.Now().Unix()), ResultOptions{StateRoot: root})["structuredContent"].(map[string]any)
	if got, ok := result["validation"].(map[string]any); !ok || got["status"] != "launch_error" {
		t.Fatalf("repository-backed job result omitted validation: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "apply_validation")); !os.IsNotExist(err) {
		t.Fatalf("unexpected durable validation record: %v", err)
	}
}

func TestValidationTimeout(t *testing.T) {
	runValidationFixture(t, "timeout", context.DeadlineExceeded)
}

func TestValidationCommandHelper(t *testing.T) {
	switch os.Getenv("PURSER_VALIDATION_COMMAND_HELPER") {
	case "pass-exit":
		for _, pkg := range []string{"one", "two", "three"} {
			_, _ = io.WriteString(os.Stdout, `{"Action":"pass","Package":"example.test/`+pkg+`"}`+"\r\nok\texample.test/"+pkg+"\r\n")
		}
		os.Exit(1)
	case "package-timeout":
		_, _ = io.WriteString(os.Stdout, `{"Action":"run","Package":"example.test/slow"}`+"\n")
		time.Sleep(30 * time.Second)
	case "worker":
		_, _ = io.WriteString(os.Stdout, "worker output before exit\n")
		child := exec.Command(os.Args[0], "-test.run=^TestValidationCommandHelper$")
		child.Env = replaceValidationEnv(os.Environ(), "PURSER_VALIDATION_COMMAND_HELPER", "grandchild")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
	case "passing-worker":
		for _, event := range []string{
			`{"Action":"start","Package":"example.test/passing"}`,
			`{"Action":"pass","Package":"example.test/passing","Test":"TestPassing"}`,
			`{"Action":"output","Package":"example.test/passing","Output":"PASS\r\n"}`,
			`{"Action":"output","Package":"example.test/passing","Output":"ok  \texample.test/passing\t1.0s\r\n"}`,
		} {
			_, _ = io.WriteString(os.Stdout, event+"\n")
		}
		time.Sleep(30 * time.Second)
	case "grandchild":
		time.Sleep(30 * time.Second)
	case "timeout":
		time.Sleep(30 * time.Second)
	}
}

func TestLimitedCaptureKeepsHeadAndTailWithinLimit(t *testing.T) {
	var output strings.Builder
	capture := &limitedCapture{file: &output, limit: 160, head: 64}
	input := strings.Repeat("h", 64) + strings.Repeat("m", 256) + "FINAL-PACKAGE-SUMMARY"
	if n, err := capture.Write([]byte(input)); err != nil || n != len(input) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := capture.Finish(); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if len(got) > 160 || !strings.HasPrefix(got, strings.Repeat("h", 64)) || !strings.Contains(got, "truncated") || !strings.HasSuffix(got, "FINAL-PACKAGE-SUMMARY") {
		t.Fatalf("captured %d bytes, missing bounded head/tail: %q", len(got), got)
	}
}

func TestTestJSONCaptureFindsFailurePastOutputLimit(t *testing.T) {
	record := &applyValidation{}
	capture := &testJSONCapture{record: record}
	if _, err := capture.Write([]byte(strings.Repeat("x", validationOutputLimit+100) + "\n")); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{
		`{"Action":"run","Package":"example.test/broken"}` + "\r\n",
		`{"Action":"fail","Test":"TestLateFailure","Package":"example.test/broken"}` + "\r\n",
		`{"Action":"fail","Package":"example.test/broken"}` + "\r\n",
	} {
		if _, err := capture.Write([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	capture.Finish()
	if record.Counts["packages_failed"] != 1 || len(record.FailedPackages) != 1 || record.FailedPackages[0] != "example.test/broken" || len(record.FailedTests) != 1 || record.FailedTests[0] != "TestLateFailure" {
		t.Fatalf("late failure not retained: %#v", record)
	}
}

func TestValidationTimeoutAndFailureAreDistinct(t *testing.T) {
	deadline := validationCommandFailureReason("go test", context.DeadlineExceeded, &applyValidation{RunningPackage: "example.test/slow"})
	failure := validationCommandFailureReason("go test", errors.New("exit status 1"), &applyValidation{})
	if validationFailureState(context.DeadlineExceeded) != "timeout" || !strings.Contains(deadline, "timed out") || !strings.Contains(deadline, "example.test/slow") {
		t.Fatalf("timeout result = %q", deadline)
	}
	if validationFailureState(errors.New("exit status 1")) != "failed" || failure != "go test failed: exit status 1; no failing package found in output" {
		t.Fatalf("failure result = %q", failure)
	}
}

func TestValidationNonzeroExitWithPassingPackagesStaysFailed(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "validation.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	env := replaceValidationEnv(os.Environ(), "PURSER_VALIDATION_COMMAND_HELPER", "pass-exit")
	record := &applyValidation{}
	err = runValidationCommand(t.TempDir(), env, []string{os.Args[0], "-test.run=^TestValidationCommandHelper$"}, 10*time.Second, log, record)
	if err == nil || validationFailureState(err) != "failed" || record.Counts["packages_passed"] != 3 {
		t.Fatalf("run = %v, counts = %#v", err, record.Counts)
	}
	if reason := validationCommandFailureReason("go test", err, record); reason != "go test failed: worker exited with code 1; no failing package found in output" {
		t.Fatalf("failure reason = %q", reason)
	}
	if _, err := log.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(log)
	if err != nil || strings.Count(string(output), "ok\texample.test/") != 3 || strings.Contains(string(output), "FAIL") {
		t.Fatalf("output = %q, error = %v", output, err)
	}
}

func TestValidationCommandExitedWhileGrandchildKeepsPipeOpen(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "validation.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	env := replaceValidationEnv(os.Environ(), "PURSER_VALIDATION_COMMAND_HELPER", "worker")
	started := time.Now()
	err = runValidationCommand(t.TempDir(), env, []string{os.Args[0], "-test.run=^TestValidationCommandHelper$"}, 10*time.Second, log, nil)
	if err != nil {
		t.Fatalf("runValidationCommand: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("command completion took %s", elapsed)
	}
	if _, err := log.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "worker output before exit") {
		t.Fatalf("output before worker exit was not captured: %q", output)
	}
}

func TestValidationCommandPassesAfterPackageSummaryAndDeadline(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "validation.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	env := replaceValidationEnv(os.Environ(), "PURSER_VALIDATION_COMMAND_HELPER", "passing-worker")
	record := &applyValidation{}
	started := time.Now()
	err = runValidationCommand(t.TempDir(), env, []string{os.Args[0], "-test.run=^TestValidationCommandHelper$"}, time.Second, log, record)
	if err != nil || record.Counts["packages_passed"] != 1 || record.RunningPackage != "" || record.Reason != "passed; leftover child process held output" {
		t.Fatalf("runValidationCommand = %v, record = %#v", err, record)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("command completion took %s, want before helper finishes", elapsed)
	}
}

func TestValidationPackageOutputCompletion(t *testing.T) {
	record := &applyValidation{}
	capture := &testJSONCapture{record: record}
	for _, event := range []string{
		`{"Action":"start","Package":"example.test/pkg"}`,
		`{"Action":"output","Package":"example.test/pkg","Output":"ok  \texample.test/pkg\t[no test files]\r\n"}`,
		`{"Action":"pass","Package":"example.test/pkg"}`,
	} {
		parseTestJSONLine([]byte(event), capture)
	}
	if record.Counts["packages_passed"] != 1 || !capture.allPackagesSucceeded() {
		t.Fatalf("package summary/pass not deduplicated: %#v", record)
	}
	for _, event := range []string{
		`{"Action":"output","Package":"example.test/pkg","Output":"FAIL\texample.test/pkg\t1.0s\r\n"}`,
		`{"Action":"fail","Package":"example.test/pkg"}`,
	} {
		parseTestJSONLine([]byte(event), capture)
	}
	if record.Counts["packages_failed"] != 1 || len(record.FailedPackages) != 1 || capture.allPackagesSucceeded() {
		t.Fatalf("package failure summary not retained/deduplicated: %#v", record)
	}
}

func TestValidationCommandDeadlineWhileProcessRunning(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "validation.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	env := replaceValidationEnv(os.Environ(), "PURSER_VALIDATION_COMMAND_HELPER", "package-timeout")
	record := &applyValidation{}
	err = runValidationCommand(t.TempDir(), env, []string{os.Args[0], "-test.run=^TestValidationCommandHelper$"}, time.Second, log, record)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runValidationCommand error = %v, want deadline exceeded", err)
	}
	if record.RunningPackage != "example.test/slow" || validationCommandFailureReason("go test", err, record) != "go test timed out while package example.test/slow was running" {
		t.Fatalf("timeout package/reason = %q / %q", record.RunningPackage, validationCommandFailureReason("go test", err, record))
	}
}

func TestValidationStaleRevision(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "main.go"), "package validation\nfunc Value() int { return 3 }\n")
	prior := runApplyValidationCommand
	called := false
	runApplyValidationCommand = func(string, []string, []string, time.Duration, io.Writer, *applyValidation) error {
		called = true
		return nil
	}
	t.Cleanup(func() { runApplyValidationCommand = prior })
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	got, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "stale" || called {
		t.Fatalf("record=%#v command_called=%t", got, called)
	}
	root, repo, record = validationFixture(t, []string{"main.go"})
	key = validationRepositoryKey(repo)
	path = validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	prior = runApplyValidationCommand
	runApplyValidationCommand = func(_ string, _ []string, args []string, _ time.Duration, _ io.Writer, result *applyValidation) error {
		if len(args) > 0 && args[0] == "test" && result != nil {
			result.Counts = map[string]int{"tests_passed": 1}
		}
		return nil
	}
	t.Cleanup(func() { runApplyValidationCommand = prior })
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "main.go"), "package validation\nfunc Value() int { return 4 }\n")
	if got := ApplyValidationResult(root, repo, record.JobID); got["status"] != "stale" {
		t.Fatalf("source edit retained validation receipt: %#v", got)
	}
}

func TestValidationCarriesStaleApplyPaths(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		covered      bool
	}{
		{name: "passed", status: "passed", covered: true},
		{name: "failed", status: "failed", covered: true},
		{name: "stale", status: "stale"},
		{name: "test_not_started", status: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, repo, record := validationFixture(t, []string{"main.go"})
			write(t, filepath.Join(repo, "pkg", "extra.go"), "package pkg\nfunc Extra() int { return 1 }\n")
			revision, snapshot, err := checkoutRevision(repo)
			if err != nil {
				t.Fatal(err)
			}
			record.Revision, record.Snapshot = revision, snapshot
			key := validationRepositoryKey(repo)
			path := validationRecordPath(root, key, record.JobID)
			if err := writeApplyValidation(path, record); err != nil {
				t.Fatal(err)
			}
			prior := applyValidation{
				JobID: strings.Repeat("b", 64), Repository: repo, Snapshot: strings.Repeat("c", 64),
				Paths: []string{filepath.Join("pkg", "extra.go")}, Status: "stale",
			}
			if err := writeApplyValidation(validationRecordPath(root, key, prior.JobID), prior); err != nil {
				t.Fatal(err)
			}
			oldRunner := runApplyValidationCommand
			var testedArgs []string
			runApplyValidationCommand = func(_ string, _ []string, args []string, _ time.Duration, _ io.Writer, _ *applyValidation) error {
				if tc.name == "test_not_started" {
					if args[0] == "vet" {
						return errors.New("vet failed")
					}
					return &exec.Error{Name: "go", Err: exec.ErrNotFound}
				}
				if len(args) > 0 && args[0] == "test" {
					testedArgs = append([]string(nil), args...)
					if tc.name == "failed" {
						return errors.New("test failed")
					}
					if tc.name == "stale" {
						write(t, filepath.Join(repo, "main.go"), "package validation\nfunc Value() int { return 99 }\n")
					}
				}
				return nil
			}
			t.Cleanup(func() { runApplyValidationCommand = oldRunner })
			if err := RunApplyValidation(root, key, record.JobID); err != nil {
				t.Fatal(err)
			}
			got, err := readApplyValidation(path)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(got.Paths, "pkg/extra.go") || (tc.name != "test_not_started" && !slices.Contains(testedArgs, "./pkg")) || got.Status != tc.status {
				t.Fatalf("carried paths=%v test args=%v", got.Paths, testedArgs)
			}
			prior, err = readApplyValidation(validationRecordPath(root, key, prior.JobID))
			if err != nil {
				t.Fatal(err)
			}
			wantCoveredBy := ""
			if tc.covered {
				wantCoveredBy = record.JobID
			}
			if prior.CoveredBy != wantCoveredBy || got.CoveredBy != wantCoveredBy {
				t.Fatalf("prior coverage=%q current coverage=%q, want %q", prior.CoveredBy, got.CoveredBy, wantCoveredBy)
			}
			next := applyValidation{JobID: strings.Repeat("d", 64), Repository: repo, Snapshot: "next", Paths: []string{"main.go"}}
			// A later apply makes completed records stale, too; those must stay covered.
			invalidatePriorApplyValidations(root, filepath.Dir(path), next.JobID, next.Snapshot)
			if err := carryUnvalidatedApplyPaths(validationRecordPath(root, key, next.JobID), &next); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(next.Paths, "pkg/extra.go") == tc.covered {
				t.Fatalf("next validation paths=%v, prior covered=%t", next.Paths, tc.covered)
			}
		})
	}
}

func TestValidationSourceEditDuringRunIsStale(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	prior := runApplyValidationCommand
	runApplyValidationCommand = func(_ string, _ []string, args []string, _ time.Duration, _ io.Writer, result *applyValidation) error {
		if len(args) > 0 && args[0] == "test" {
			write(t, filepath.Join(repo, "main.go"), "package validation\nfunc Value() int { return 99 }\n")
			if result != nil {
				result.Counts = map[string]int{"tests_passed": 1}
			}
		}
		return nil
	}
	t.Cleanup(func() { runApplyValidationCommand = prior })
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	got, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "stale" {
		t.Fatalf("source edit during validation was not stale: %#v", got)
	}
}

func TestValidationCommitDuringRunKeepsPassedResult(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	prior := runApplyValidationCommand
	runApplyValidationCommand = func(_ string, _ []string, args []string, _ time.Duration, _ io.Writer, result *applyValidation) error {
		if len(args) > 0 && args[0] == "test" {
			git(t, repo, "add", "--all")
			git(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-qm", "applied changes")
			if result != nil {
				result.Counts = map[string]int{"tests_passed": 1}
			}
		}
		return nil
	}
	t.Cleanup(func() { runApplyValidationCommand = prior })
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	got, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" {
		t.Fatalf("commit of validated content changed status: %#v", got)
	}
}

func TestValidationRepeatApplyEnqueuesOnce(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	prior := launchApplyValidation
	launches := 0
	launchApplyValidation = func([]string, []string, string) (int, int64, error) { launches++; return 42, 1, nil }
	t.Cleanup(func() { launchApplyValidation = prior })
	job := map[string]any{"jobId": record.JobID, "repoRoot": repo}
	for i := 0; i < 2; i++ {
		value := map[string]any{"applied": true, "paths": record.Paths}
		enqueueAppliedValidation(root, job, value)
	}
	if launches != 1 {
		t.Fatalf("launches=%d", launches)
	}
}

func TestValidationInterruptedOnRestart(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	path := validationRecordPath(root, validationRepositoryKey(repo), record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	RecoverApplyValidations(root)
	got, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "interrupted" || got.Reason != "validation process no longer matches its recorded identity" {
		t.Fatalf("record=%+v", got)
	}
}

func TestValidationWaitsForRepositoryValidationLock(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	unlock, err := contextio.LockFileBlocking(filepath.Join(root, "apply_validation", key, "repository.lock"))
	if err != nil {
		t.Fatal(err)
	}
	prior := runApplyValidationCommand
	runApplyValidationCommand = func(_ string, _ []string, args []string, _ time.Duration, _ io.Writer, result *applyValidation) error {
		if len(args) > 0 && args[0] == "test" && result != nil {
			result.Counts = map[string]int{"tests_passed": 1}
		}
		return nil
	}
	t.Cleanup(func() {
		runApplyValidationCommand = prior
		_ = unlock()
	})
	finished := make(chan error, 1)
	go func() { finished <- RunApplyValidation(root, key, record.JobID) }()
	time.Sleep(11 * time.Second)
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("validation exited while queued behind another validation: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("validation did not finish after repository lock was released")
	}
	got, err := readApplyValidation(path)
	if err != nil || got.Status != "passed" {
		t.Fatalf("validation after lock release = %#v, %v", got, err)
	}
}

func TestValidationMissingPluginBinaryIsSkipped(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"hooks/register.ts"})
	if err := os.MkdirAll(filepath.Join(repo, "hooks"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "hooks", "register.ts"), "export {}\n")
	revision, snapshot, err := checkoutRevision(repo)
	if err != nil {
		t.Fatal(err)
	}
	record.Revision, record.Snapshot = revision, snapshot
	key := validationRepositoryKey(repo)
	path := validationRecordPath(root, key, record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(gitBin))
	if err := RunApplyValidation(root, key, record.JobID); err != nil {
		t.Fatal(err)
	}
	got, err := readApplyValidation(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "skipped" || got.Reason != "claude binary is unavailable" {
		t.Fatalf("record=%#v", got)
	}
	if summary := validationSummary(got); summary["plugin_test"].(map[string]any)["status"] != "skipped" {
		t.Fatalf("plugin absence was not recorded separately: %#v", summary)
	}
}

func TestApplyValidationOutcomeSurvivesStaleness(t *testing.T) {
	for _, status := range []string{"passed", "failed", "timeout"} {
		t.Run(status, func(t *testing.T) {
			root, repo, record := validationFixture(t, []string{"main.go"})
			record.TaskID, record.Route, record.Model = "repair-task", "normal", "gpt-test"
			record.StartedAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			record.FailedTests = []string{strings.Repeat("T", 600)}
			record.Packages = make([]string, 101)
			for i := range record.Packages {
				record.Packages[i] = "./pkg"
			}
			path := validationRecordPath(root, validationRepositoryKey(repo), record.JobID)
			if err := writeApplyValidation(path, record); err != nil {
				t.Fatal(err)
			}
			// A concurrent checkout change invalidates the receipt, but not the historical outcome.
			write(t, filepath.Join(repo, "main.go"), "package validation\nfunc Value() int { return 3 }\n")
			for range 2 {
				if err := finishValidation(root, path, &record, status, "go test outcome"); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := readApplyValidation(path)
			if err != nil || stored.Status != "stale" {
				t.Fatalf("receipt = %#v, %v", stored, err)
			}
			data, err := os.ReadFile(filepath.Join(root, "telemetry", "events", "by_task", record.TaskID+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != 1 {
				t.Fatalf("outcomes = %d: %s", len(lines), data)
			}
			var event struct {
				TaskID    string         `json:"task_id"`
				Type      string         `json:"event_type"`
				Timestamp string         `json:"ts_utc"`
				Payload   map[string]any `json:"payload"`
			}
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			p := event.Payload
			if event.TaskID != record.TaskID || event.Type != "APPLY_VALIDATION_FINISHED" || event.Timestamp != record.FinishedAt || p["job_id"] != record.JobID || p["route"] != record.Route || p["model"] != record.Model || p["applied_snapshot"] != record.Snapshot || p["applied_revision"] != record.Revision || p["validation_status"] != status || p["validation_reason"] != "go test outcome" || p["duration_ms"].(float64) < 1000 {
				t.Fatalf("outcome = %#v", event)
			}
			if len(p["failing_tests"].([]any)[0].(string)) != 512 || len(p["packages"].([]any)) != 100 {
				t.Fatalf("unbounded payload: %#v", p)
			}
			canonical, err := os.ReadFile(filepath.Join(root, "telemetry", "events", record.FinishedAt[:7], "session_unknown.jsonl"))
			if err != nil || string(canonical) != string(data) {
				t.Fatalf("canonical event differs: %s, %v", canonical, err)
			}
		})
	}
}

func TestApplyValidationOutcomeWriteFailureKeepsTerminalReceipt(t *testing.T) {
	root, repo, record := validationFixture(t, []string{"main.go"})
	path := validationRecordPath(root, validationRepositoryKey(repo), record.JobID)
	if err := writeApplyValidation(path, record); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "telemetry"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "telemetry", "events"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := finishValidation(root, path, &record, "passed", ""); err != nil {
		t.Fatalf("telemetry failure affected validation result: %v", err)
	}
	stored, err := readApplyValidation(path)
	if err != nil || stored.Status != "passed" || stored.Reason != "" {
		t.Fatalf("terminal receipt = %#v, %v", stored, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "telemetry", "telemetry_health.json"))
	if err != nil {
		t.Fatal(err)
	}
	var health struct {
		Status     string `json:"status"`
		Components map[string]struct {
			Status    string `json:"status"`
			ErrorCode string `json:"last_error_code"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &health); err != nil {
		t.Fatal(err)
	}
	ingestion := health.Components["ingestion"]
	if health.Status != "DEGRADED" || ingestion.Status != "DEGRADED" || !strings.HasPrefix(ingestion.ErrorCode, "INGESTION_EMIT_") {
		t.Fatalf("telemetry health = %s", data)
	}
}
