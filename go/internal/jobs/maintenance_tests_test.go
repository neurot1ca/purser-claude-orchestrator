package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"purser-hook/internal/proc"
)

func TestMaintenanceTestDeltaGuards(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	baselineRaw, err := exec.Command("git", "-C", root, "mktree").Output()
	if err != nil {
		t.Fatalf("empty baseline tree: %v", err)
	}
	baseline := strings.TrimSpace(string(baselineRaw))
	write := func(path, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	identity := MaintenanceTestIdentity{JobID: "job-1", RecordedID: "job-1", RecordedBy: "host"}
	check := func(paths ...string) error {
		t.Helper()
		return ValidateMaintenanceTestDelta(root, map[string]any{"status": "OK", "baseline_head": baseline, "changed_paths": paths}, identity)
	}
	write("pkg/example_test.go", "package pkg\nimport \"testing\"\nfunc TestExample(t *testing.T) { if 1 != 1 { t.Error(\"bad\") } }\n")
	if err := check("pkg/example_test.go"); err != nil {
		t.Fatalf("valid test delta rejected: %v", err)
	}
	write("pkg/production.go", "package pkg\n")
	if err := check("pkg/production.go"); err == nil {
		t.Fatal("production change accepted")
	}
	identity.RecordedBy = "prompt"
	if err := ValidateMaintenanceTestDelta(root, map[string]any{"status": "OK", "baseline_head": baseline, "changed_paths": []string{"pkg/example_test.go"}}, identity); err == nil {
		t.Fatal("untrusted maintenance identity accepted")
	}
	identity.RecordedBy = "host"
	write("pkg/vacuous_test.go", "package pkg\nimport \"testing\"\nfunc TestVacuous(t *testing.T) { t.Skip(\"always\") }\n")
	if err := check("pkg/vacuous_test.go"); err == nil {
		t.Fatal("unconditional skipped test accepted")
	}
}

func TestMaintenanceTestPathRecognition(t *testing.T) {
	for _, path := range []string{"x_test.go", "test_a.py", "a_test.py", "a.test.ts", "a.spec.js", "testdata/fixture.json"} {
		if !maintenanceTestPath(path) {
			t.Errorf("test path not recognized: %s", path)
		}
	}
	for _, path := range []string{"main.go", "config.json", "package.json", "a.testdata.js"} {
		if maintenanceTestPath(path) {
			t.Errorf("non-test path recognized: %s", path)
		}
	}
}

func TestMaintenanceModeUsesGitFilemode(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := proc.Hide(exec.Command("git", append([]string{"-C", root}, args...)...)).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	path := filepath.Join(root, "mode_test.go")
	if err := os.WriteFile(path, []byte("package example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "mode_test.go")
	run("update-index", "--chmod=+x", "mode_test.go")
	baseline := run("write-tree")
	run("config", "core.filemode", "false")
	if err := checkMaintenanceMode(root, baseline, "mode_test.go"); err != nil {
		t.Fatalf("Git reports no mode change with core.filemode=false: %v", err)
	}
	t.Run("executable mode change", func(t *testing.T) {
		run("update-index", "--chmod=-x", "mode_test.go")
		baseline := run("write-tree")
		if err := os.Chmod(path, 0755); err != nil {
			t.Skipf("cannot set executable filesystem mode: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&0111 == 0 {
			t.Skip("filesystem cannot express an executable mode")
		}
		run("config", "core.filemode", "true")
		if err := checkMaintenanceMode(root, baseline, "mode_test.go"); err == nil {
			t.Fatal("Git executable mode change was accepted")
		}
	})
}

func TestMaintenanceTestHistoryAndExecutedIdentityCount(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	path := filepath.Join(root, "pkg_test.go")
	old := "package pkg\nimport \"testing\"\nfunc TestStable(t *testing.T) { t.Error(\"a\"); t.Fatal(\"b\") }\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "pkg_test.go")
	run("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := strings.TrimSpace(run("rev-parse", "HEAD"))
	current := "package pkg\nimport \"testing\"\nfunc TestStable(t *testing.T) { t.Error(\"a\") }\n"
	if err := os.WriteFile(path, []byte(current), 0600); err != nil {
		t.Fatal(err)
	}
	if err := compareMaintenanceGoTests(root, base, "pkg_test.go", path); err == nil {
		t.Fatal("weakened prior test accepted")
	}
	if got := CountExecutedMaintenanceTests([]string{"TestA", "TestB", "TestA"}, []string{"TestA", "TestA", "TestB/case1", "unknown"}); got != 1 {
		t.Fatalf("executed identities=%d want 1", got)
	}
}
