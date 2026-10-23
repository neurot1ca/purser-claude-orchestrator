package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestJobWorktreePythonFixture(t *testing.T) {
	file := filepath.Join("testdata", "job_worktree.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name    string         `json:"name"`
			Trusted bool           `json:"trusted"`
			Copy    string         `json:"copy"`
			Want    map[string]any `json:"want"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			base, err := filepath.Abs(gitTempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			repo, parent := filepath.Join(base, "repo"), filepath.Join(base, "jobs")
			if err := os.MkdirAll(repo, 0700); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "init")
			git(t, repo, "config", "user.name", "fixture")
			git(t, repo, "config", "user.email", "fixture@example.invalid")
			git(t, repo, "config", "core.autocrlf", "false")
			writePatchTestFile(t, filepath.Join(repo, "tracked.txt"), []byte("base\n"))
			git(t, repo, "add", "tracked.txt")
			git(t, repo, "commit", "-m", "base")
			for name, body := range map[string]string{"tracked.txt": "dirty\n", "new.txt": "untracked\n", ".gitignore": "ignored.dat\n", "ignored.dat": "ignored\n", ".orchestrator/profile.toml": "schema_version = 1\n[worktree]\ncopy = [\"" + tc.Copy + "\"]\n"} {
				writePatchTestFile(t, filepath.Join(repo, filepath.FromSlash(name)), []byte(body))
			}
			home := filepath.Join(base, "home")
			user := filepath.Join(home, ".orchestrator", "config.toml")
			config := "schema_version = 1\n[trust]\npaths = [\"" + filepath.ToSlash(repo) + "\"]\n"
			if !tc.Trusted {
				config = "schema_version = 1\n[trust]\npaths = []\n"
			}
			writePatchTestFile(t, user, []byte(config))
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			got, createErr := CreateJobWorktree(repo, "job-worktree-fixture", parent)
			actual := map[string]any{"head": "", "snapshot": "", "files": map[string]string{}, "error": createErr != nil, "cleaned": false, "index_matches_snapshot": false}
			if createErr == nil {
				actual["head"], actual["snapshot"] = "@HEAD@", "@SNAPSHOT@"
				actual["files"] = worktreeFiles(t, got["path"])
				tree, err := runGit(got["path"], []string{"write-tree"}, time.Second, "checking job worktree index", nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				actual["index_matches_snapshot"] = strings.TrimSpace(string(tree)) == got["snapshot"]
				if len(got["head"]) < 40 || len(got["snapshot"]) < 40 {
					t.Fatalf("missing hashes: %#v", got)
				}
			}
			_, statErr := os.Stat(filepath.Join(parent, "job-worktree"))
			actual["cleaned"] = os.IsNotExist(statErr) && strings.Count(stringsOutput(t, repo, "worktree", "list", "--porcelain"), "worktree ") == 1
			want, _ := json.Marshal(tc.Want)
			actualJSON, _ := json.Marshal(actual)
			if !reflect.DeepEqual(actualJSON, want) {
				t.Fatalf("worktree = %s, want %s", actualJSON, want)
			}
		})
	}
}

func worktreeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if !info.IsDir() && filepath.ToSlash(rel) != ".git" {
			b, e := os.ReadFile(p)
			if e == nil {
				files[filepath.ToSlash(rel)] = string(b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestJobWorktreeCopiesManyUntrackedFixtureFiles(t *testing.T) {
	base := gitTempDir(t)
	repo, parent := filepath.Join(base, "repo"), filepath.Join(base, "jobs")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "fixture")
	git(t, repo, "config", "user.email", "fixture@example.invalid")
	git(t, repo, "config", "core.longpaths", "true")
	writePatchTestFile(t, filepath.Join(repo, "tracked.txt"), []byte("base\n"))
	git(t, repo, "add", "tracked.txt")
	git(t, repo, "commit", "-m", "base")

	for i := 0; i < 190; i++ {
		name := filepath.Join("Runtime", "tests", fmt.Sprintf(".fixture-%02d", i%5), "nested", fmt.Sprintf("case-%03d.txt", i))
		writePatchTestFile(t, filepath.Join(repo, name), []byte("fixture"))
	}
	readonly := filepath.Join(repo, "Runtime", "tests", ".fixture-readonly", "readonly.txt")
	writePatchTestFile(t, readonly, []byte("readonly"))
	if err := os.Chmod(readonly, 0444); err != nil {
		t.Fatal(err)
	}
	readonlyBefore, err := os.Stat(readonly)
	if err != nil {
		t.Fatal(err)
	}
	longRel := filepath.Join("Runtime", "tests", ".fixture-long")
	for i := 0; i < 7; i++ {
		longRel = filepath.Join(longRel, strings.Repeat("x", 36))
	}
	longRel = filepath.Join(longRel, "case.txt")
	longPath := filepath.Join(repo, longRel)
	if len(longPath) <= 260 {
		t.Fatalf("long path length = %d, want > 260", len(longPath))
	}
	writePatchTestFile(t, longPath, []byte("long path"))
	raw, err := runGit(repo, []string{"ls-files", "--others", "--exclude-standard", "-z"}, time.Second, "listing fixture files", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if len(listed) != 192 {
		t.Fatalf("listed untracked file count = %d, want 192", len(listed))
	}
	for _, name := range listed {
		if strings.HasSuffix(name, "/") {
			t.Fatalf("ordinary fixture listed as a directory: %q", name)
		}
	}

	result, err := CreateJobWorktree(repo, "many-fixtures", parent)
	if err != nil {
		t.Fatalf("CreateJobWorktree: %v", err)
	}
	files := worktreeFiles(t, result["path"])
	if len(files) != 193 {
		t.Fatalf("copied file count = %d, want 193", len(files))
	}
	if got := files[filepath.ToSlash(longRel)]; got != "long path" {
		t.Fatalf("long path content = %q", got)
	}
	if got := files["Runtime/tests/.fixture-readonly/readonly.txt"]; got != "readonly" {
		t.Fatalf("read-only file content = %q", got)
	}
	for i := 0; i < 190; i++ {
		name := filepath.ToSlash(filepath.Join("Runtime", "tests", fmt.Sprintf(".fixture-%02d", i%5), "nested", fmt.Sprintf("case-%03d.txt", i)))
		if got := files[name]; got != "fixture" {
			t.Fatalf("fixture %s content = %q", name, got)
		}
	}
	readonlyAfter, err := os.Stat(readonly)
	if err != nil {
		t.Fatal(err)
	}
	if readonlyBefore.Mode() != readonlyAfter.Mode() {
		t.Fatalf("read-only source mode changed from %v to %v", readonlyBefore.Mode(), readonlyAfter.Mode())
	}
	if got, err := os.ReadFile(readonly); err != nil || string(got) != "readonly" {
		t.Fatalf("read-only source content = %q, err = %v", got, err)
	}
}

func TestJobWorktreeRejectsEmbeddedGitMetadata(t *testing.T) {
	base := gitTempDir(t)
	repo, parent := filepath.Join(base, "repo"), filepath.Join(base, "jobs")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "fixture")
	git(t, repo, "config", "user.email", "fixture@example.invalid")
	writePatchTestFile(t, filepath.Join(repo, "tracked.txt"), []byte("base\n"))
	git(t, repo, "add", "tracked.txt")
	git(t, repo, "commit", "-m", "base")
	embedded := filepath.Join(repo, "Runtime", "tests", ".fixture-embedded")
	if err := os.MkdirAll(embedded, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, embedded, "init")
	writePatchTestFile(t, filepath.Join(embedded, "nested.txt"), []byte("embedded"))
	listed := stringsOutput(t, repo, "ls-files", "--others", "--exclude-standard", "-z")
	if listed != "Runtime/tests/.fixture-embedded/\x00" {
		t.Fatalf("embedded repository enumeration = %q", listed)
	}

	_, err := CreateJobWorktree(repo, "embedded", parent)
	if err == nil || !strings.Contains(err.Error(), "UNTRACKED_FILE_UNSUPPORTED") || !strings.Contains(err.Error(), "Runtime/tests/.fixture-embedded") {
		t.Fatalf("CreateJobWorktree error = %v, want unsupported embedded repository path", err)
	}
	if _, statErr := os.Stat(filepath.Join(parent, "embedded")); !os.IsNotExist(statErr) {
		t.Fatalf("failed worktree remains after embedded repository rejection: %v", statErr)
	}
}

func TestUntrackedSnapshotSkipsVanishedSources(t *testing.T) {
	repo, worktree := initGitRepository(t), initGitRepository(t)
	leaf := filepath.Join(repo, "Runtime", "tests", ".fixture-leaf", "a.txt")
	parent := filepath.Join(repo, "Runtime", "tests", ".fixture-parent", "a.txt")
	replaced := filepath.Join(repo, "Runtime", "tests", ".fixture-replaced", "a.txt")
	for _, name := range []string{leaf, parent, replaced, filepath.Join(repo, "survivor.txt")} {
		writePatchTestFile(t, name, []byte("fixture"))
	}
	raw, err := runGit(repo, []string{"ls-files", "--others", "--exclude-standard", "-z"}, time.Second, "listing fixture files", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "\x00") != 4 {
		t.Fatalf("expected four listed files, got %q", raw)
	}
	// Delete a leaf, remove a parent, and replace another parent with a file
	// after enumeration, covering missing files/paths and ENOTDIR.
	for _, name := range []string{leaf, parent, filepath.Dir(parent), replaced, filepath.Dir(replaced)} {
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	writePatchTestFile(t, filepath.Dir(replaced), []byte("replacement"))
	if err := copyUntrackedFiles(repo, worktree, raw); err != nil {
		t.Fatalf("copying listed files after deletion: %v", err)
	}
	git(t, worktree, "add", "-A")
	snapshot := strings.TrimSpace(stringsOutput(t, worktree, "write-tree"))
	if got := stringsOutput(t, worktree, "ls-tree", "-r", "--name-only", snapshot); strings.TrimSpace(got) != "survivor.txt" {
		t.Fatalf("snapshot files = %q, want only survivor.txt", got)
	}
	if got, err := os.ReadFile(filepath.Join(worktree, "survivor.txt")); err != nil || string(got) != "fixture" {
		t.Fatalf("surviving file = %q, err = %v", got, err)
	}
}

func TestUntrackedCopyDestinationFailureKeepsRelativePath(t *testing.T) {
	repo, worktree := gitTempDir(t), gitTempDir(t)
	const name = "Runtime/tests/.fixture-copy/a.txt"
	writePatchTestFile(t, filepath.Join(repo, filepath.FromSlash(name)), []byte("fixture"))
	writePatchTestFile(t, filepath.Join(worktree, "Runtime"), []byte("blocks destination directory"))
	if err := copyUntrackedFiles(repo, worktree, []byte(name+"\x00")); err == nil || !strings.Contains(err.Error(), name) {
		t.Fatalf("copy error = %v, want failure with relative path", err)
	}
}
