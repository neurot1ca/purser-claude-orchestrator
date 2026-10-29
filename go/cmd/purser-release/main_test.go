package main

import "purser-hook/internal/testutil"

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func releaseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	commands := [][]string{{"init", "-q"}, {"config", "user.name", "Release Test"}, {"config", "user.email", "release@example.test"}, {"config", "core.autocrlf", "false"}}
	for _, args := range commands {
		cmd := testutil.Hide(exec.Command("git", append([]string{"-C", root}, args...)...))
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v: %s", args, e, b)
		}
	}
	write := func(name, data string) {
		t.Helper()
		p := filepath.Join(root, name)
		if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(data), 0644); e != nil {
			t.Fatal(e)
		}
	}
	write(".claude-plugin/plugin.json", "{\n  \"name\": \"purser\",\n  \"version\": \"0.1.12\"\n}\n")
	write("CHANGELOG.md", "# Changelog\n\n## 0.1.12 — 2026-09-24\n\n- Existing (`aaaaaaa`).\n")
	write("change.txt", "before\n")
	write("other.txt", "other\n")
	cmd := testutil.Hide(exec.Command("git", "-C", root, "add", "."))
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("git add: %v: %s", e, b)
	}
	cmd = testutil.Hide(exec.Command("git", "-C", root, "-c", "user.name=Release Test", "-c", "user.email=release@example.test", "commit", "-qm", "base"))
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("git commit: %v: %s", e, b)
	}
	return root
}
func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := testutil.Hide(exec.Command("git", append([]string{"-C", root}, args...)...))
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}

func TestReleaseDryRunWithPathsDoesNotCommitOrChangeVersion(t *testing.T) {
	root := releaseFixture(t)
	if e := os.WriteFile(filepath.Join(root, "change.txt"), []byte("after\n"), 0644); e != nil {
		t.Fatal(e)
	}
	before := gitOutput(t, root, "rev-parse", "HEAD")
	if _, e := releaseVersion(root, "0.1.13", "Added change", "add change", true, filepath.Join(t.TempDir(), "market"), []string{"change.txt"}, "feature: add change"); e != nil {
		t.Fatal(e)
	}
	if got := gitOutput(t, root, "rev-parse", "HEAD"); got != before {
		t.Fatalf("dry run moved HEAD: %s -> %s", before, got)
	}
	b, e := os.ReadFile(filepath.Join(root, ".claude-plugin/plugin.json"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), `"version": "0.1.12"`) {
		t.Fatalf("dry run changed plugin version: %s", b)
	}
}

func TestReleasePathsCommitOnlyListedChangeAndResumeAfterReleaseCommit(t *testing.T) {
	root := releaseFixture(t)
	if e := os.WriteFile(filepath.Join(root, "change.txt"), []byte("selected\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "other.txt"), []byte("unselected edit\n"), 0644); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "market")
	commit, e := releaseVersion(root, "0.1.13", "Added change", "add change", false, target, []string{"change.txt"}, "feature: add change")
	if e != nil {
		t.Fatal(e)
	}
	if got := gitOutput(t, root, "log", "-1", "--format=%s"); got != "purser: release 0.1.13 (add change)" {
		t.Fatalf("unexpected release commit: %s", got)
	}
	check := testutil.Hide(exec.Command("git", "-C", root, "diff", "--quiet", "--", "other.txt"))
	if err := check.Run(); err == nil {
		t.Fatal("unlisted edit was committed")
	}
	changelog, e := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if e != nil {
		t.Fatal(e)
	}
	changeCommit := gitOutput(t, root, "rev-parse", "HEAD^")
	if !strings.Contains(string(changelog), "Added change (`"+changeCommit[:7]+"`).") {
		t.Fatalf("changelog omitted change commit hash: %s", changelog)
	}
	if strings.Index(string(changelog), "## 0.1.13 — ") < 0 || strings.Index(string(changelog), "## 0.1.13 — ") > strings.Index(string(changelog), "## 0.1.12 — ") {
		t.Fatalf("new changelog entry was not inserted before the newest entry: %s", changelog)
	}
	again, e := releaseVersion(root, "", "Added change", "add change", false, target, nil, "")
	if e != nil || again != commit {
		t.Fatalf("release retry did not resume HEAD: %s %v", again, e)
	}
}

func TestReleaseRejectsUnrelatedStagedFileWithoutChanges(t *testing.T) {
	root := releaseFixture(t)
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("selected\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("staged elsewhere\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := testutil.Hide(exec.Command("git", "-C", root, "add", "--", "unrelated.txt")).CombinedOutput(); err != nil {
		t.Fatalf("stage unrelated file: %v: %s", err, out)
	}
	beforeHead := gitOutput(t, root, "rev-parse", "HEAD")
	beforeIndex := gitOutput(t, root, "diff", "--cached", "--binary")
	if _, err := releaseVersion(root, "0.1.13", "Added change", "add change", false, filepath.Join(t.TempDir(), "market"), []string{"change.txt"}, "feature: add change"); err == nil || !strings.Contains(err.Error(), "staged changes outside --paths") {
		t.Fatalf("unrelated staged file was not refused: %v", err)
	}
	if got := gitOutput(t, root, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("refused release moved HEAD: %s -> %s", beforeHead, got)
	}
	if got := gitOutput(t, root, "diff", "--cached", "--binary"); got != beforeIndex {
		t.Fatalf("refused release changed index:\nbefore %q\nafter %q", beforeIndex, got)
	}
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(changelog), "## 0.1.13 — ") {
		t.Fatalf("refused release changed changelog: %s", changelog)
	}
}

func TestFailedPluginUpdateResumesWithoutNewCommits(t *testing.T) {
	root := releaseFixture(t)
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("selected\n"), 0644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "market")
	commit, err := releaseVersion(root, "0.1.13", "Added change", "add change", false, target, []string{"change.txt"}, "feature: add change")
	if err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(t.TempDir(), "fake-claude")
	if runtime.GOOS == "windows" {
		claude += ".exe"
	}
	source := filepath.Join(t.TempDir(), "fake-claude.go")
	program := "package main\nimport (\"fmt\"; \"os\"; \"strings\")\nfunc main() { args := strings.Join(os.Args[1:], \" \" ); fmt.Println(\"fake claude:\", args); if strings.Contains(args, \"purser@purser-local\") { if code := os.Getenv(\"FAKE_CLAUDE_EXIT\"); code != \"\" && code != \"0\" { fmt.Println(\"controlled updater failure\"); os.Exit(7) } } }\n"
	if err := os.WriteFile(source, []byte(program), 0644); err != nil {
		t.Fatal(err)
	}
	build := testutil.Hide(exec.Command("go", "build", "-o", claude, source))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude: %v: %s", err, out)
	}
	t.Setenv("FAKE_CLAUDE_EXIT", "7")
	if err := runPluginUpdates(root, claude); err == nil || !strings.Contains(err.Error(), "plugin update purser@purser-local") {
		t.Fatalf("expected controlled plugin update failure, got %v", err)
	}
	failed, err := releaseVersion(root, "0.1.13", "Added change", "add change", false, target, nil, "")
	if err != nil || failed != commit {
		t.Fatalf("failed plugin update retry did not resume release commit: got %s want %s: %v", failed, commit, err)
	}
	t.Setenv("FAKE_CLAUDE_EXIT", "0")
	if err := runPluginUpdates(root, claude); err != nil {
		t.Fatalf("plugin update retry failed: %v", err)
	}
	if got := gitOutput(t, root, "rev-list", "--count", "HEAD"); got != "3" {
		t.Fatalf("retry created commits after failed plugin update: count=%s", got)
	}
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(changelog), "## 0.1.13 — "); got != 1 {
		t.Fatalf("retry duplicated changelog entry: count=%d", got)
	}
}

func TestExpandPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{"glob", []string{"--paths", filepath.Join("src", "*.go"), "other.txt", "--dry-run"}, []string{"--path-item=" + filepath.Join("src", "*.go"), "--path-item=other.txt", "--dry-run"}},
		{"separators", []string{"--paths", `src\nested\file.go`, "src/nested/file.go"}, []string{`--path-item=src\nested\file.go`, "--path-item=src/nested/file.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandPaths(tc.in)
			if err != nil || strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("expandPaths(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
	if _, err := expandPaths([]string{"--paths", "--dry-run"}); err == nil || !strings.Contains(err.Error(), "requires at least one path") {
		t.Fatalf("missing paths error = %v", err)
	}
}

func TestStringListFlag(t *testing.T) {
	var values stringList
	if got := values.String(); got != "" {
		t.Fatalf("empty String() = %q", got)
	}
	for _, value := range []string{"one", "two"} {
		if err := values.Set(value); err != nil {
			t.Fatal(err)
		}
	}
	if got := values.String(); got != "one,two" {
		t.Fatalf("String() = %q; want one,two", got)
	}
}

func runFixture(t *testing.T) string {
	t.Helper()
	root := releaseFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "go"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go", "go.mod"), []byte("module fixture\n\ngo 1.23\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := testutil.Hide(exec.Command("git", "-C", root, "add", "go/go.mod"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add fixture module: %v: %s", err, out)
	}
	cmd = testutil.Hide(exec.Command("git", "-C", root, "-c", "user.name=Release Test", "-c", "user.email=release@example.test", "commit", "-qm", "fixture module"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit fixture module: %v: %s", err, out)
	}
	return root
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func TestRunFlagsAndArgumentErrors(t *testing.T) {
	root := runFixture(t)
	chdir(t, root)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"package", "--unknown"}, "flag provided but not defined"},
		{[]string{"smoke", "--unknown"}, "flag provided but not defined"},
		{[]string{"catalog"}, "catalog requires --url"},
		{[]string{"--unknown"}, "flag provided but not defined"},
		{[]string{"--entry=", "--title=", "--marketplace-dir", t.TempDir()}, "--entry and --title must not be empty"},
		{[]string{"--entry=Added", "--title=Release", "--message=change", "--marketplace-dir", t.TempDir()}, "--message requires --paths"},
		{[]string{"--export-only", "--version=1.2.3", "--marketplace-dir", t.TempDir()}, "cannot be combined"},
	} {
		if err := run(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("run(%q) error = %v; want containing %q", tc.args, err, tc.want)
		}
	}
	if err := run([]string{"--dry-run", "--entry=Added", "--title=Release", "--marketplace-dir", t.TempDir(), "--claude-bin", "must-not-run"}); err != nil {
		t.Fatalf("dry-run with parsed flags: %v", err)
	}
}

func TestRepositoryRootInsideAndOutside(t *testing.T) {
	root := runFixture(t)
	nested := filepath.Join(root, "go")
	chdir(t, nested)
	got, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	got, _ = filepath.EvalSymlinks(got)
	if got != want {
		t.Fatalf("repositoryRoot() = %q; want %q", got, want)
	}
	outside := t.TempDir()
	chdir(t, outside)
	if _, err := repositoryRoot(); err == nil || !strings.Contains(err.Error(), "cannot locate repository root") {
		t.Fatalf("outside repositoryRoot() error = %v", err)
	}
}

func TestDefaultMarketplaceUsesPlatformHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "user-home")
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	want := filepath.Join(home, ".claude", "purser", "marketplace")
	if got := defaultMarketplace(); got != want {
		t.Fatalf("defaultMarketplace() = %q; want %q", got, want)
	}
}
