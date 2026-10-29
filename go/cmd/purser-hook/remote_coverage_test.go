package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"purser-hook/internal/testutil"
)

func TestRemoteContent(t *testing.T) {
	want := []any{"text"}
	for _, input := range []map[string]any{nil, {}, {"content": "wrong type"}} {
		if got := remoteContent(input); got != nil {
			t.Fatalf("remoteContent(%#v) = %#v, want nil", input, got)
		}
	}
	if got := remoteContent(map[string]any{"content": want}); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("remoteContent dropped content: %#v", got)
	}
}

func TestRemoteCommitMessageError(t *testing.T) {
	for _, args := range []map[string]any{nil, {}, {"commitMessage": 1}, {"commitMessage": " \t "}, {"commitMessage": strings.Repeat("a", 201)}, {"commitMessage": "café"}} {
		if got := remoteCommitMessageError(args); got == "" {
			t.Errorf("accepted invalid commit message: %#v", args)
		}
	}
	if got := remoteCommitMessageError(map[string]any{"commitMessage": strings.Repeat("a", 200)}); got != "" {
		t.Fatalf("rejected valid commit message: %s", got)
	}
}

func TestRemoteCommitAppliedRefusals(t *testing.T) {
	repo := t.TempDir()
	for _, tc := range []struct {
		name, want   string
		args, result map[string]any
	}{
		{"tool error", "", map[string]any{}, map[string]any{"isError": true}},
		{"not applied", "", map[string]any{}, map[string]any{"structuredContent": map[string]any{"applied": false}}},
		{"bad message", "commitMessage", map[string]any{"commitMessage": ""}, map[string]any{"structuredContent": map[string]any{"applied": true, "paths": []any{"file"}}}},
		{"path outside repo", "invalid applied path", map[string]any{"commitMessage": "valid"}, map[string]any{"structuredContent": map[string]any{"applied": true, "paths": []any{"../outside"}}}},
		{"absolute path", "invalid applied path", map[string]any{"commitMessage": "valid"}, map[string]any{"structuredContent": map[string]any{"applied": true, "paths": []any{filepath.Join(repo, "file")}}}},
		{"no paths", "applied job returned no paths", map[string]any{"commitMessage": "valid"}, map[string]any{"structuredContent": map[string]any{"applied": true, "paths": []any{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := remoteCommitApplied(repo, tc.args, tc.result)
			if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("remoteCommitApplied() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRemoteCommitAppliedCommitsAndPushes(t *testing.T) {
	root := t.TempDir()
	repo, bare := filepath.Join(root, "repo"), filepath.Join(root, "remote.git")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	gitMCPTest(t, repo, "init", "-q")
	gitMCPTest(t, repo, "config", "user.name", "fixture")
	gitMCPTest(t, repo, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "change.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitMCPTest(t, repo, "add", "change.txt")
	gitMCPTest(t, repo, "commit", "-m", "base")
	if err := os.Mkdir(bare, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := testutil.Hide(exec.Command("git", "init", "--bare", "-q", bare))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init bare: %v: %s", err, out)
	}
	gitMCPTest(t, repo, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(repo, "change.txt"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"structuredContent": map[string]any{"applied": true, "paths": []any{"change.txt"}}}
	if got := remoteCommitApplied(repo, map[string]any{"commitMessage": "remote apply"}, result); got != "" {
		t.Fatalf("commit failed: %s", got)
	}
	committed := strings.ReplaceAll(string(gitMCPOutput(t, bare, "show", "HEAD:change.txt")), "\r\n", "\n")
	if result["commit"] == nil || committed != "after\n" {
		t.Fatalf("commit was not recorded and pushed: %#v", result)
	}
	unchanged := map[string]any{"structuredContent": map[string]any{"applied": true, "paths": []any{"change.txt"}}}
	if got := remoteCommitApplied(repo, map[string]any{"commitMessage": "nothing to commit"}, unchanged); !strings.Contains(got, "git commit failed") {
		t.Fatalf("unchanged path did not refuse empty commit: %q", got)
	}
}

func TestRemoteHostValidationCoverage(t *testing.T) {
	state, repo := t.TempDir(), t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PURSER_STATE_DIR", state)
	c := &remoteConfig{repo: repo}
	for _, id := range []string{"", "../bad", strings.Repeat("g", 64)} {
		if got := remoteHostValidation(c, map[string]any{"jobId": id}); got["isError"] != true {
			t.Errorf("accepted invalid job ID %q: %#v", id, got)
		}
	}
	id := strings.Repeat("b", 64)
	if got := remoteHostValidation(c, map[string]any{"jobId": id}); got["isError"] != true {
		t.Fatalf("missing validation record accepted: %#v", got)
	}
	abs, _ := filepath.Abs(repo)
	key := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(abs))))
	dir := filepath.Join(state, "apply_validation", hex.EncodeToString(key[:]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"jobId": id, "status": "passed"}
	b, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	got := remoteHostValidation(c, map[string]any{"jobId": id})
	if got["isError"] == true || got["structuredContent"].(map[string]any)["status"] != "passed" {
		t.Fatalf("validation record not returned: %#v", got)
	}
}

func TestRemoteHostDiffCoverage(t *testing.T) {
	state, worktree := t.TempDir(), t.TempDir()
	worktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PURSER_STATE_DIR", state)
	c := &remoteConfig{repo: worktree}
	for _, args := range []map[string]any{nil, {}, {"jobId": "../bad"}} {
		if got := remoteHostDiff(c, args); got["isError"] != true {
			t.Errorf("accepted malformed job: %#v", got)
		}
	}
	if got := remoteHostDiff(c, map[string]any{"jobId": "missing"}); got["isError"] != true {
		t.Fatalf("unknown job accepted: %#v", got)
	}
	gitMCPTest(t, worktree, "init", "-q")
	gitMCPTest(t, worktree, "config", "user.name", "fixture")
	gitMCPTest(t, worktree, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitMCPTest(t, worktree, "add", "tracked.txt")
	gitMCPTest(t, worktree, "commit", "-m", "base")
	snapshot := strings.TrimSpace(string(gitMCPOutput(t, worktree, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("c", 64)
	jobPath := filepath.Join(state, "codex_exec_mcp", "jobs", id+".json")
	if err := os.MkdirAll(filepath.Dir(jobPath), 0700); err != nil {
		t.Fatal(err)
	}
	writeJob := func(v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := os.WriteFile(jobPath, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJob(map[string]any{"worktreePath": worktree})
	if got := remoteHostDiff(c, map[string]any{"jobId": id}); got["isError"] != true {
		t.Fatalf("job without snapshot accepted: %#v", got)
	}
	writeJob(map[string]any{"worktreePath": worktree, "snapshot": snapshot})
	got := remoteHostDiff(c, map[string]any{"jobId": id})
	diff := strings.ReplaceAll(got["content"].([]any)[0].(map[string]any)["text"].(string), "\r\n", "\n")
	if got["isError"] == true || !strings.Contains(diff, "after") {
		t.Fatalf("host diff missing changed content: %#v", got)
	}
}
