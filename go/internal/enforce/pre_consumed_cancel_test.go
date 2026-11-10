package enforce_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"purser-hook/internal/enforce"
	"purser-hook/internal/jobs"
)

func TestPreConsumedCancel(t *testing.T) {
	if os.Getenv("PURSER_CONSUMED_CANCEL_STUB") == "1" {
		time.Sleep(time.Minute)
		return
	}
	pid, created, err := jobs.LaunchDetached([]string{os.Args[0], "-test.run=^TestPreConsumedCancel$"},
		append(os.Environ(), "PURSER_CONSUMED_CANCEL_STUB=1"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if jobs.IdentityMatches(pid, created) {
			child, _ := os.FindProcess(pid)
			if child != nil {
				_ = child.Kill()
				_ = child.Release()
			}
		}
	})
	for _, field := range []string{"hostPid", "supervisorPid", "pid", "agentPid", "none", "reused", "unidentified"} {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			call := "consumed-cancel"
			sum := sha256.Sum256([]byte(call))
			id := hex.EncodeToString(sum[:])
			job := map[string]any{"jobId": id, "call": call, "state": "RUNNING"}
			if identityField := map[string]string{"hostPid": "hostCreatedAt", "supervisorPid": "supervisorCreatedAt", "pid": "pidCreatedAt", "agentPid": "agentCreatedAt"}[field]; identityField != "" {
				job[field] = pid
				job[identityField] = created
			}
			if field == "reused" {
				job["agentPid"], job["agentCreatedAt"] = pid, created+1
			}
			if field == "unidentified" {
				job["hostPid"] = pid
			}
			for path, value := range map[string]any{
				filepath.Join(root, "async_jobs", id+".json"): map[string]any{
					"jobId": id, "call": call, "call_key": id, "project_key": "project", "session_id": "session", "stage": "CONSUMED",
				},
				filepath.Join(root, "codex_exec_mcp", "jobs", id+".json"): job,
			} {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := enforce.Pre(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "mcp__codex-main__codex_cancel", "session_id": "session",
				"tool_input": map[string]any{"jobId": id}}, root)
			if err != nil {
				t.Fatal(err)
			}
			if field == "none" || field == "reused" || field == "unidentified" {
				output, _ := got["hookSpecificOutput"].(map[string]any)
				if output["permissionDecision"] != "deny" || output["permissionDecisionReason"] != "ASYNC_ALREADY_CONSUMED: This job has already been finalized." {
					t.Fatalf("consumed dead job: %#v", got)
				}
			} else if len(got) != 0 {
				t.Fatalf("consumed live job: %#v", got)
			}
		})
	}
	if !jobs.IdentityMatches(pid, created) {
		t.Fatal("PID reuse check stopped the unrelated stub")
	}
}
