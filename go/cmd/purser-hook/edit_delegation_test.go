package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"purser-hook/internal/projectkey"
	"purser-hook/internal/testutil"
)

func TestEditDelegationHookProcess(t *testing.T) {
	repo := t.TempDir()
	state := t.TempDir()
	cmd := testutil.Hide(exec.Command("git", "init", "-q", repo))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "source.go"), []byte("package source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = testutil.Hide(exec.Command("git", "-C", repo, "add", "source.go"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %s %v", output, err)
	}
	roles := filepath.Join(state, "projects", projectkey.For(repo), "roles.toml")
	if err := os.MkdirAll(filepath.Dir(roles), 0700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PURSER_STATE_DIR", state)
	t.Setenv("PURSER_WORKER", "")
	for _, malformed := range []bool{false, true} {
		text := "[roles]\ncoder = 'codex'\n"
		if malformed {
			text = "[roles\n"
		}
		if err := os.WriteFile(roles, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		for _, tool := range []string{"Write", "Bash"} {
			if malformed && tool == "Bash" {
				continue
			}
			input := map[string]any{"file_path": "new.go", "content": "package main"}
			if tool == "Bash" {
				input = map[string]any{"command": `sed -i 's/a/b/g' source.go`}
			}
			raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "session_id": "main", "cwd": repo, "tool_input": input})
			cmd := testutil.Hide(exec.Command(self, "enforce"))
			cmd.Stdin = bytes.NewReader(raw)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if malformed {
				if err == nil || len(output) != 0 || stderr.Len() == 0 {
					t.Fatalf("%s malformed roles must signal failure without deny: %q %q %v", tool, output, stderr.String(), err)
				}
			} else if err != nil || !bytes.Contains(output, []byte(`"permissionDecision":"deny"`)) {
				t.Fatalf("%s deny missing: %q %q %v", tool, output, stderr.String(), err)
			}
		}
	}
}
