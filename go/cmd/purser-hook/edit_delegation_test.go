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
		raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Write", "session_id": "main", "cwd": repo,
			"tool_input": map[string]any{"file_path": "new.go", "content": "package main"}})
		cmd := testutil.Hide(exec.Command(self, "enforce"))
		cmd.Stdin = bytes.NewReader(raw)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if malformed {
			if err == nil || len(output) != 0 || stderr.Len() == 0 {
				t.Fatalf("error must signal failure without deny: %q %q %v", output, stderr.String(), err)
			}
		} else {
			if err != nil || !bytes.Contains(output, []byte(`"permissionDecision":"deny"`)) {
				t.Fatalf("deny missing: %q %q %v", output, stderr.String(), err)
			}
		}
	}
}
