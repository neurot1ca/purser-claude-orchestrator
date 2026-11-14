package contextio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/waiter"
)

func TestFollowupWaiterInstructions(t *testing.T) {
	root := t.TempDir()
	call, session, tool, project := "call", "session", "tool", "project"
	jobID := hashText(call)
	callKey := hashText(session + "\x00" + tool)
	write := func(path string, value map[string]any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "async_jobs", jobID+".json"), map[string]any{
		"jobId": jobID, "call": call, "call_key": callKey, "project_key": project, "session_id": session,
	})
	write(filepath.Join(root, "pending", callKey+".json"), map[string]any{
		"job_id": jobID, "async_call": call, "session_id": session, "project_key": project,
		"tool_use_id": tool, "task_id": "task",
	})
	write(filepath.Join(root, "codex_exec_mcp", "jobs", jobID+".json"), map[string]any{
		"jobId": jobID, "call": call, "state": "RUNNING",
	})

	if got := followupHints(root, project, session, "startup", ""); !strings.Contains(got,
		"Start "+waiter.Command(jobID)+" in the background (use the PowerShell tool on Windows); call codex_result when it finishes, without adopt or asking the user.") {
		t.Fatalf("running job hint = %q", got)
	}
	if got := stallInstruction(jobID, nil); !strings.Contains(got,
		"Restart "+waiter.Command(jobID)+" in the background (use the PowerShell tool on Windows); continue waiting.") {
		t.Fatalf("stall instruction = %q", got)
	}
}

func followupHintFixture(t *testing.T, state string, finishedAt any) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	call, session, tool, project := "call", "session", "tool", "project"
	jobID := hashText(call)
	callKey := hashText(session + "\x00" + tool)
	write := func(path string, value map[string]any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "async_jobs", jobID+".json"), map[string]any{
		"jobId": jobID, "call": call, "call_key": callKey, "project_key": project, "session_id": session,
	})
	write(filepath.Join(root, "pending", callKey+".json"), map[string]any{
		"job_id": jobID, "async_call": call, "session_id": session, "project_key": project,
		"tool_use_id": tool, "task_id": "task",
	})
	job := map[string]any{"jobId": jobID, "call": call, "state": state}
	if finishedAt != nil {
		job["finishedAt"] = finishedAt
	}
	write(filepath.Join(root, "codex_exec_mcp", "jobs", jobID+".json"), job)
	return root, project, session
}

func TestFollowupHintsSkipsOldFinishedJob(t *testing.T) {
	root, project, session := followupHintFixture(t, "SUCCEEDED", time.Now().UTC().Add(-48*time.Hour).Unix())
	if got := followupHints(root, project, session, "startup", ""); got != "" {
		t.Fatalf("old finished job hint = %q, want no hint", got)
	}
}

func TestFollowupHintsSurfacesRecentFinishedJob(t *testing.T) {
	root, project, session := followupHintFixture(t, "SUCCEEDED", time.Now().UTC().Add(-time.Hour).Unix())
	if got := followupHints(root, project, session, "startup", ""); !strings.Contains(got, "Call codex_result") {
		t.Fatalf("recent finished job hint = %q, want codex_result instruction", got)
	}
}

func TestFollowupHintsSurfacesRunningJob(t *testing.T) {
	root, project, session := followupHintFixture(t, "RUNNING", nil)
	if got := followupHints(root, project, session, "startup", ""); !strings.Contains(got, "call codex_result when it finishes") {
		t.Fatalf("running job hint = %q, want wait instruction", got)
	}
}

func setForeignFollowupOwner(t *testing.T, root, session, owner string) {
	t.Helper()
	jobID, callKey, foreignCallKey := hashText("call"), hashText(session+"\x00tool"), hashText("other-session\x00tool")
	for path, fields := range map[string]map[string]any{
		filepath.Join(root, "async_jobs", jobID+".json"): {"session_id": "other-session", "call_key": foreignCallKey, "session_owner": owner},
		filepath.Join(root, "pending", callKey+".json"):  {"session_id": "other-session"},
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		for key, field := range fields {
			value[key] = field
		}
		data, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(filepath.Join(root, "pending", callKey+".json"), filepath.Join(root, "pending", foreignCallKey+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestFollowupHintsSkipsForeignJobOnCompact(t *testing.T) {
	root, project, session := followupHintFixture(t, "RUNNING", nil)
	setForeignFollowupOwner(t, root, session, "")
	if got := followupHints(root, project, session, "compact", ""); got != "" {
		t.Fatalf("foreign compact hint = %q, want no hint", got)
	}
}

func TestFollowupHintsSkipsForeignJobWithLiveOwner(t *testing.T) {
	root, project, session := followupHintFixture(t, "RUNNING", nil)
	owner, err := waiter.PinOwner(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	setForeignFollowupOwner(t, root, session, owner.String())
	if got := followupHints(root, project, session, "startup", ""); got != "" {
		t.Fatalf("foreign live-owner hint = %q, want no hint", got)
	}
}

func TestFollowupHintsSurfacesRecentForeignJobWithUnknownOwnerOnStartup(t *testing.T) {
	root, project, session := followupHintFixture(t, "RUNNING", nil)
	setForeignFollowupOwner(t, root, session, "unknown")
	if got := followupHints(root, project, session, "startup", ""); !strings.Contains(got, "The Codex task I started before the app closed") {
		t.Fatalf("foreign startup hint = %q, want existing follow-up text", got)
	}
}

func TestFollowupHintsKeepsSameSessionJobOnCompact(t *testing.T) {
	root, project, session := followupHintFixture(t, "RUNNING", nil)
	if got := followupHints(root, project, session, "compact", ""); !strings.Contains(got, "call codex_result when it finishes") {
		t.Fatalf("same-session compact hint = %q, want waiter instruction", got)
	}
}

func TestFollowupHintsSurfacesForeignJobWithDeadOwnerAndOldActivity(t *testing.T) {
	root, project, session := followupHintFixture(t, "RUNNING", nil)
	setForeignFollowupOwner(t, root, session, "2147483647:1")
	writeSessionMaintenanceJSON(t, filepath.Join(root, "codex_exec_mcp", "jobs", hashText("call")+".json"), map[string]any{
		"jobId": hashText("call"), "call": "call", "state": "RUNNING",
		"liveness": map[string]any{"lastActivityAt": time.Now().Add(-48 * time.Hour).Unix()},
	})
	if got := followupHints(root, project, session, "startup", ""); !strings.Contains(got, "The Codex task I started before the app closed") {
		t.Fatalf("foreign dead-owner hint = %q, want existing follow-up text despite old activity", got)
	}
}
