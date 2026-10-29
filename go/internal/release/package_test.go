package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPackageFromTemporaryRepository(t *testing.T) {
	root := t.TempDir()
	files := append(append(append([]string{}, modSources...), runtimeFiles...), commandFiles...)
	for _, name := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		data := []byte("fixture\n")
		if name == ".claude-plugin/plugin.json" {
			data = []byte(`{"version":"1.2.3"}`)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".claude-plugin/plugin.json", "go/go.mod", "go/cmd/purser-hook/main.go", "go/internal/enforce/code_map_grammar_tags.go"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		data := []byte("package main\nfunc main() {}\n")
		switch name {
		case ".claude-plugin/plugin.json":
			data = []byte(`{"version":"1.2.3"}`)
		case "go/go.mod":
			data = []byte("module fixture\n\ngo 1.23\n")
		case "go/internal/enforce/code_map_grammar_tags.go":
			data = []byte("package enforce\nconst codeMapGrammarTags = \"fixture\"\n")
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "fixture@example.invalid")
	runGit("config", "user.name", "Fixture")
	runGit("add", ".")
	runGit("commit", "-qm", "fixture")
	commit, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Package(root, string(commit[:len(commit)-1]), t.TempDir())
	if err != nil {
		t.Fatalf("Package failed: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("package archive missing: %v", err)
	}
}
