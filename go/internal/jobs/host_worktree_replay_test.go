package jobs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"purser-hook/internal/ledger"
)

func TestStartWorktreePreservesCheckoutThroughReplayAndReply(t *testing.T) {
	parent := filepath.Join("..", "..", "..", ".tmp")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(parent, "host-worktree-replay-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, err = filepath.Abs(base)
	if err != nil {
		t.Fatal(err)
	}
	repo, state := filepath.Join(base, "repo"), filepath.Join(base, "state")
	if err = os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	gitStart(t, repo, "init", "-q")
	gitStart(t, repo, "config", "core.autocrlf", "false")
	write(t, filepath.Join(repo, "A.txt"), "base A\n")
	write(t, filepath.Join(repo, "B.txt"), "base B\n")
	write(t, filepath.Join(repo, ".gitignore"), "build/\n")
	gitStart(t, repo, "add", "-A")
	gitStart(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	write(t, filepath.Join(repo, "A.txt"), "checkout A\n")
	write(t, filepath.Join(repo, "B.txt"), "checkout B\n")
	gitStart(t, repo, "add", "B.txt")
	write(t, filepath.Join(repo, "new.txt"), "prior uncommitted addition\n")
	write(t, filepath.Join(repo, "cache.pyc"), "untracked bytecode")
	if err = os.Mkdir(filepath.Join(repo, "build"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "build", "output.bin"), "ignored build output")
	check := func(root, name, want string) {
		t.Helper()
		got, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil || string(got) != want {
			t.Errorf("%s in %s = %q, err=%v; want %q", name, root, got, readErr, want)
		}
	}
	t.Setenv("PURSER_HOST_FAKE_WORKER", "1")
	t.Setenv("PURSER_HOST_FAKE_MODE", "")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "codex_exec_mcp", "jobs")
	var id string
	reply := false
	options := StartOptions{StateRoot: state, Validation: StartValidationOptions{Agent: "codex", FixedAgent: true},
		Launch: func(jobID string, generation int64, token string) error {
			id = jobID
			_, hostErr := RunHostGeneration(dir, jobID, generation, token, HostOptions{StateRoot: state,
				WorkerCommand: func(args LaunchArgs) ([]string, error) {
					check(args.Cwd, "A.txt", "checkout A\n")
					if reply {
						check(args.Cwd, "B.txt", "worker B\n")
						write(t, filepath.Join(args.Cwd, "A.txt"), "reply A\n")
					} else {
						check(args.Cwd, "B.txt", "checkout B\n")
						write(t, filepath.Join(args.Cwd, "B.txt"), "worker B\n")
					}
					check(args.Cwd, "new.txt", "prior uncommitted addition\n")
					for _, name := range []string{"cache.pyc", filepath.Join("build", "output.bin")} {
						if _, statErr := os.Lstat(filepath.Join(args.Cwd, name)); !os.IsNotExist(statErr) {
							t.Errorf("junk path %s was seeded: %v", name, statErr)
						}
					}
					return []string{executable, "-test.run=TestHostFakeWorker", "--", args.OutputPath}, nil
				}})
			return hostErr
		}}
	response, _, err := Start(state, map[string]any{"call": "00000000-0000-4000-8000-000000000001", "cwd": repo, "worktree": true,
		"prompt": "ORCH|task=seed|phase=implementation|route=normal|scope=B.txt\nUpdate B."}, options)
	if err != nil || id == "" {
		t.Fatalf("start response=%#v err=%v", response, err)
	}
	job := recordMap(readJob(dir, id))
	if job["state"] != "SUCCEEDED" {
		t.Fatalf("host job=%#v", job)
	}
	worktree, snapshot := nonemptyString(job["worktreePath"]), nonemptyString(job["snapshot"])
	prepared, err := ledger.PrepareJobReplay(repo, worktree, snapshot, filepath.Join(state, "preparations", id))
	if err != nil || prepared["status"] != "prepared" {
		t.Fatalf("first replay=%#v err=%v", prepared, err)
	}
	check(worktree, "A.txt", "checkout A\n")
	check(worktree, "B.txt", "worker B\n")
	check(worktree, "new.txt", "prior uncommitted addition\n")
	snapshot = prepared["snapshot"].(string)
	delta := ledger.ComputeJobDelta(worktree, snapshot)
	if !reflect.DeepEqual(delta["changed_paths"], []string{"B.txt"}) {
		t.Errorf("scope check after replay: %#v", delta)
	}
	// Result finalization records this snapshot before codex_start resumes the thread.
	stored := readJob(dir, id)
	if err = stored.Set("snapshot", snapshot); err != nil {
		t.Fatal(err)
	}
	if err = Write(filepath.Join(dir, id+".json"), stored); err != nil {
		t.Fatal(err)
	}
	reply = true
	response, _, err = Start(state, map[string]any{"call": "00000000-0000-4000-8000-000000000002", "mode": "reply", "threadId": "fake-thread",
		"prompt": "ORCH|task=seed|phase=implementation|route=normal|scope=A.txt,B.txt\nUpdate A."}, options)
	if err != nil || recordMap(readJob(dir, id))["state"] != "SUCCEEDED" {
		t.Fatalf("reply start response=%#v err=%v", response, err)
	}
	snapshot = nonemptyString(recordMap(readJob(dir, id))["snapshot"])
	replied, err := ledger.PrepareJobReplay(repo, worktree, snapshot, filepath.Join(state, "preparations", id))
	if err != nil || replied["status"] != "prepared" {
		t.Fatalf("reply replay=%#v err=%v", replied, err)
	}
	check(worktree, "A.txt", "reply A\n")
	revision, err := ledger.JobChangedPathsSHA256(worktree, replied["snapshot"].(string))
	if err != nil || len(revision) != 64 || strings.TrimSpace(revision) == "" {
		t.Fatalf("review revision=%q err=%v", revision, err)
	}
}
