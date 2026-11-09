package enforce

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/proc"
	"purser-hook/internal/projectkey"
)

func TestEditDelegation(t *testing.T) {
	for _, name := range []string{
		"write", "edit", "multi", "notebook", "trivial", "replace-one-line", "replace-many-lines",
		"prose", "no-coder", "none", "claude", "opt-out", "missing-roles", "bad-roles", "bad-opt-out",
		"unreadable-roles", "worker", "dispatcher", "ignored", "outside", "new-path", "subdirectory",
		"git-failure", "missing-git", "failed-session", "other-session", "conflict", "bad-index", "other-tool",
		"large-directory", "old-conflict", "old-replay", "older-failure", "latest-failure", "latest-timeout", "latest-stage-failure", "cancelled", "tied-success",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PURSER_WORKER", "")
			base := t.TempDir()
			repo, state := filepath.Join(base, "repo"), filepath.Join(base, "state")
			write := func(path, text string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(repo, "source.go"), "old old\nold\n")
			cmd := proc.Hide(exec.Command("git", "init", "-q", repo))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git init: %s: %v", output, err)
			}
			rolesPath := filepath.Join(state, "projects", projectkey.For(repo), "roles.toml")
			write(rolesPath, "[roles]\ncoder = 'codex'\n")
			input := map[string]any{"file_path": "source.go", "old_string": "old\nold", "new_string": "new\nnew"}
			event := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Write", "session_id": "main", "cwd": repo, "tool_input": input}
			deny := false
			switch name {
			case "write":
				deny = true
			case "edit":
				event["tool_name"], deny = "Edit", true
			case "multi":
				event["tool_name"], deny = "MultiEdit", true
			case "notebook":
				event["tool_name"], input["notebook_path"], deny = "NotebookEdit", "work.ipynb", true
			case "trivial":
				event["tool_name"], input["old_string"], input["new_string"] = "Edit", "old", "new"
			case "replace-one-line", "replace-many-lines":
				event["tool_name"], input["old_string"], input["new_string"], input["replace_all"] = "Edit", "old", "new", true
				if name == "replace-one-line" {
					write(filepath.Join(repo, "source.go"), "old old\nother\n")
				} else {
					deny = true
				}
			case "prose":
				input["file_path"] = "README.md"
			case "no-coder":
				write(rolesPath, "[roles]\n")
			case "none", "claude":
				write(rolesPath, "[roles]\ncoder = '"+name+"'\n")
			case "opt-out":
				write(rolesPath, "[roles]\ncoder = 'codex'\nenforce_delegation = false\n")
			case "missing-roles":
				if err := os.Remove(rolesPath); err != nil {
					t.Fatal(err)
				}
			case "bad-roles":
				write(rolesPath, "[roles\n")
			case "bad-opt-out":
				write(rolesPath, "[roles]\ncoder = 'codex'\nenforce_delegation = 'false'\n")
			case "unreadable-roles":
				if err := os.Remove(rolesPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(rolesPath, 0700); err != nil {
					t.Fatal(err)
				}
			case "worker":
				t.Setenv("PURSER_WORKER", "1")
			case "dispatcher":
				event["agent_id"] = "dispatcher-1"
			case "ignored":
				write(filepath.Join(repo, ".gitignore"), "source.go\n")
			case "outside":
				input["file_path"] = filepath.Join(base, "elsewhere.go")
			case "new-path":
				input["file_path"], deny = "new/nested/test.go", true
			case "subdirectory":
				sub := filepath.Join(repo, "sub")
				if err := os.Mkdir(sub, 0700); err != nil {
					t.Fatal(err)
				}
				event["cwd"], input["file_path"], deny = sub, "../source.go", true
			case "git-failure":
				if err := os.Rename(filepath.Join(repo, ".git"), filepath.Join(repo, ".git-disabled")); err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(repo, ".git"), "gitdir: missing\n")
			case "missing-git":
				t.Setenv("PATH", base)
			case "failed-session", "other-session", "conflict", "bad-index", "old-conflict", "old-replay", "older-failure", "latest-failure", "latest-timeout", "latest-stage-failure", "cancelled", "tied-success":
				id := strings.Repeat("a", 64)
				index := map[string]any{"project_key": projectkey.For(repo), "session_id": "main", "jobId": id, "call": "call"}
				job := map[string]any{"jobId": id, "call": "call", "state": "FAILED"}
				if name == "other-session" {
					index["session_id"], deny = "other", true
				}
				if name == "conflict" || name == "old-conflict" || name == "old-replay" {
					job["state"], job["applyState"] = "SUCCEEDED", "conflict"
					if name != "conflict" {
						index["session_id"], deny = "other", true
					}
					if name == "old-replay" {
						delete(job, "applyState")
						job["applyConflictReason"] = "worker_replay"
					}
				}
				if name == "latest-failure" {
					job["state"] = "SUCCEEDED"
				}
				if name == "cancelled" {
					job["state"], deny = "CANCELLED", true
				}
				if name == "latest-timeout" {
					job["state"] = "TIMED_OUT"
				}
				if name == "latest-stage-failure" {
					job["state"], index["stage"] = "RUNNING", "FAILED"
				}
				raw, _ := json.Marshal(index)
				write(filepath.Join(state, "async_jobs", id+".json"), string(raw))
				raw, _ = json.Marshal(job)
				write(filepath.Join(state, "codex_exec_mcp", "jobs", id+".json"), string(raw))
				if name == "older-failure" || name == "latest-failure" || name == "tied-success" {
					first := filepath.Join(state, "async_jobs", id+".json")
					stamp := time.Now().Add(-time.Hour)
					if err := os.Chtimes(first, stamp, stamp); err != nil {
						t.Fatal(err)
					}
					secondID := strings.Repeat("b", 64)
					index["jobId"], job["jobId"] = secondID, secondID
					if name == "latest-failure" {
						job["state"] = "FAILED"
						stamp = stamp.Add(time.Minute)
					} else {
						job["state"], deny = "SUCCEEDED", true
						if name != "tied-success" {
							stamp = stamp.Add(time.Minute)
						}
					}
					raw, _ = json.Marshal(index)
					second := filepath.Join(state, "async_jobs", secondID+".json")
					write(second, string(raw))
					if err := os.Chtimes(second, stamp, stamp); err != nil {
						t.Fatal(err)
					}
					raw, _ = json.Marshal(job)
					write(filepath.Join(state, "codex_exec_mcp", "jobs", secondID+".json"), string(raw))
				}
				if name == "bad-index" {
					write(filepath.Join(state, "async_jobs", id+".json"), "broken")
				}
			case "large-directory":
				deny = true
				stamp := time.Now().Add(-time.Hour)
				for i := 0; i < 300; i++ {
					path := filepath.Join(state, "async_jobs", fmt.Sprintf("%03d.json", i))
					text := "{}"
					if i < 44 {
						text = "broken"
					}
					write(path, text)
					mtime := stamp.Add(time.Duration(i) * time.Second)
					if err := os.Chtimes(path, mtime, mtime); err != nil {
						t.Fatal(err)
					}
					write(path+".lock", "")
					write(path+".claim", "")
				}
			case "other-tool":
				event["tool_name"] = "Bash"
			}
			response, err := Pre(event, state)
			if got := response != nil; got != deny {
				t.Fatalf("deny=%v, want %v; response=%v error=%v", got, deny, response, err)
			}
			if deny && mapValue(response["hookSpecificOutput"])["permissionDecisionReason"] != "Delegate this code change to the selected coder through the purser:dispatcher agent or codex_start." {
				t.Fatalf("wrong deny: %v", response)
			}
		})
	}
}

func TestShellEditDelegation(t *testing.T) {
	for _, tc := range []struct {
		name, roles                     string
		worker, agent                   string
		tracked, ignored, outside, deny bool
	}{
		{name: "tracked denied", roles: "[roles]\ncoder='codex'\n", tracked: true, deny: true},
		{name: "untracked allowed", roles: "[roles]\ncoder='codex'\n"},
		{name: "ignored allowed", roles: "[roles]\ncoder='codex'\n", tracked: true, ignored: true},
		{name: "outside allowed", roles: "[roles]\ncoder='codex'\n", outside: true},
		{name: "claude mode", roles: "[roles]\ncoder='claude'\n", tracked: true},
		{name: "none mode", roles: "[roles]\ncoder='none'\n", tracked: true},
		{name: "delegation disabled", roles: "[roles]\ncoder='codex'\nenforce_delegation=false\n", tracked: true},
		{name: "worker", roles: "[roles]\ncoder='codex'\n", worker: "1", tracked: true},
		{name: "agent", roles: "[roles]\ncoder='codex'\n", agent: "subagent", tracked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PURSER_WORKER", tc.worker)
			base := t.TempDir()
			repo := filepath.Join(base, "repo")
			state := filepath.Join(base, "state")
			if err := os.MkdirAll(repo, 0700); err != nil {
				t.Fatal(err)
			}
			write := func(path, value string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(repo, "source.go"), "package source\n")
			cmd := proc.Hide(exec.Command("git", "init", "-q", repo))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git init: %s: %v", out, err)
			}
			if tc.tracked {
				cmd = proc.Hide(exec.Command("git", "-C", repo, "add", "source.go"))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git add: %s: %v", out, err)
				}
			}
			if tc.ignored {
				write(filepath.Join(repo, ".gitignore"), "source.go\n")
			}
			write(filepath.Join(state, "projects", projectkey.For(repo), "roles.toml"), tc.roles)
			path := "source.go"
			if tc.outside {
				path = filepath.Join(base, "outside.go")
				write(path, "package outside\n")
			}
			event := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "session_id": "shell-session", "cwd": repo, "agent_id": tc.agent,
				"tool_input": map[string]any{"command": "sed -i 's/a/b/g' " + path}}
			response, err := Pre(event, state)
			if err != nil {
				t.Fatalf("Pre: %v", err)
			}
			if got := response != nil; got != tc.deny {
				t.Fatalf("deny=%v want %v response=%v", got, tc.deny, response)
			}
			if tc.deny && mapValue(response["hookSpecificOutput"])["permissionDecisionReason"] != editDelegationReason {
				t.Fatalf("reason mismatch: %v", response)
			}
		})
	}
}

func TestEditDelegationPathIdentity(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "Repository")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"symlink-inside", "symlink-outside", "dangling", "case", "long"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(repo, name+".go")
			inside := true
			switch name {
			case "symlink-inside", "symlink-outside", "dangling":
				destination := repo
				if name == "symlink-outside" {
					destination, inside = base, false
				}
				if name == "dangling" {
					destination = filepath.Join(base, "missing")
				}
				if err := os.Symlink(destination, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				if name != "dangling" {
					path = filepath.Join(path, "new.go")
				}
			case "case":
				path = filepath.Join(base, "repository", "new.go")
				_, err := os.Stat(filepath.Join(base, "repository"))
				inside = err == nil // use the actual volume's case behavior
				if runtime.GOOS == "windows" {
					path = strings.ReplaceAll(path, `\`, "/")
				}
			case "long":
				path = filepath.Join(repo, strings.Repeat("nested/", 40), "test.go")
			}
			resolved, err := editResolvedPath(path)
			if name == "dangling" {
				if err == nil {
					t.Fatal("dangling symlink resolved")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, got := editRelative(repo, resolved)
			if got != inside {
				t.Fatalf("inside=%v, want %v for %s", got, inside, resolved)
			}
		})
	}
}

func TestEditDelegationRecoveryDeadline(t *testing.T) {
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "async_jobs"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	allowed, err := editRecovery(ctx, state, "project", "session")
	if allowed || err == nil {
		t.Fatalf("expired scan must return an error for fail-open: %v %v", allowed, err)
	}
}
