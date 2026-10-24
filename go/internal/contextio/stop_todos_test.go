package contextio

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStopTodoSnapshotBlocksForOpenItems(t *testing.T) {
	transcript, repo := stopTodoFixture(t)
	appendPlanStopRecord(t, transcript, stopTodoWrite([]any{
		map[string]any{"content": "Finish implementation", "status": "in_progress"},
		map[string]any{"content": "Write docs", "status": "pending"},
	}))
	if got := stopTodoRun(t, repo, transcript, false); !strings.Contains(got, "Approved work is still open in your task list: Finish implementation, Write docs.") {
		t.Fatalf("Stop reason = %q, want open todo reminder", got)
	}
}

func TestStopTodoSnapshotCompletedItemsDoNotBlock(t *testing.T) {
	transcript, repo := stopTodoFixture(t)
	appendPlanStopRecord(t, transcript, stopTodoWrite([]any{map[string]any{"content": "Finished", "status": "completed"}}))
	if got := stopTodoRun(t, repo, transcript, false); got != "" {
		t.Fatalf("completed todo blocked Stop: %q", got)
	}
}

func TestStopTodoQuestionTurnDoesNotBlock(t *testing.T) {
	transcript, repo := stopTodoFixture(t)
	appendPlanStopRecord(t, transcript, stopTodoWrite([]any{map[string]any{"content": "Still open", "status": "pending"}}))
	appendPlanStopRecord(t, transcript, map[string]any{"type": "assistant", "message": map[string]any{"content": "Should I continue?"}})
	if got := stopTodoRun(t, repo, transcript, false); got != "" {
		t.Fatalf("question turn blocked Stop: %q", got)
	}
}

func TestStopTodoReminderDedupesWithinUserTurn(t *testing.T) {
	transcript, repo := stopTodoFixture(t)
	appendPlanStopRecord(t, transcript, stopTodoWrite([]any{map[string]any{"content": "Still open", "status": "pending"}}))
	if got := stopTodoRun(t, repo, transcript, false); got == "" {
		t.Fatal("first Stop did not remind about open todo")
	}
	if got := stopTodoRun(t, repo, transcript, false); got != "" {
		t.Fatalf("duplicate Stop blocked same user turn: %q", got)
	}
}

func TestStopTaskReplayCompletedDoesNotBlock(t *testing.T) {
	transcript, repo := stopTodoFixture(t)
	appendPlanStopRecord(t, transcript, stopTodoWrite([]any{map[string]any{"content": "Old snapshot item", "status": "pending"}}))
	appendPlanStopRecord(t, transcript, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "create-call", "name": "TaskCreate", "input": map[string]any{"subject": "Finish implementation", "description": "Complete it"}},
	}}})
	appendPlanStopRecord(t, transcript, map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "create-call", "content": "Task #task-1 created successfully"},
	}}})
	appendPlanStopRecord(t, transcript, stopTaskTool("TaskUpdate", map[string]any{"taskId": "task-1", "status": "completed"}))
	if got := stopTodoRun(t, repo, transcript, false); got != "" {
		t.Fatalf("completed task replay blocked Stop: %q", got)
	}
}

func TestStopLatestTodoSnapshotSupersedesTaskReplay(t *testing.T) {
	transcript, repo := stopTodoFixture(t)
	appendPlanStopRecord(t, transcript, stopTaskTool("TaskCreate", map[string]any{"taskId": "task-1", "subject": "Old task replay"}))
	appendPlanStopRecord(t, transcript, stopTodoWrite([]any{map[string]any{"content": "Finished", "status": "completed"}}))
	if got := stopTodoRun(t, repo, transcript, false); got != "" {
		t.Fatalf("older task replay blocked after completed TodoWrite snapshot: %q", got)
	}
}

func TestStopPlanReminderSuppressesTodoReason(t *testing.T) {
	root, repo, artifact := planFixture(t)
	t.Setenv("PURSER_STATE_DIR", root)
	if _, err := AcceptPlan(root, repo, "plan-1", artifact, []int{1}); err != nil {
		t.Fatal(err)
	}
	transcript := t.TempDir() + "/transcript.jsonl"
	appendPlanStopRecord(t, transcript, map[string]any{"type": "user", "message": map[string]any{"content": "Continue the approved work."}})
	appendPlanStopRecord(t, transcript, stopTodoWrite([]any{map[string]any{"content": "Other open work", "status": "pending"}}))
	got := stopTodoRun(t, repo, transcript, false)
	if !strings.Contains(got, "This turn, start every listed ready pass") || strings.Contains(got, "Approved work is still open in your task list") {
		t.Fatalf("Stop reason = %q, want plan reminder without todo reason", got)
	}
}

func stopTodoFixture(t *testing.T) (transcript, repo string) {
	t.Helper()
	root, repo, _ := planFixture(t)
	t.Setenv("PURSER_STATE_DIR", root)
	transcript = t.TempDir() + "/transcript.jsonl"
	appendPlanStopRecord(t, transcript, map[string]any{"type": "user", "message": map[string]any{"content": "Please complete the approved work."}})
	return transcript, repo
}

func stopTodoWrite(todos []any) map[string]any {
	return stopTaskTool("TodoWrite", map[string]any{"todos": todos})
}

func stopTaskTool(name string, input map[string]any) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "name": name, "input": input},
	}}}
}

func stopTodoRun(t *testing.T, repo, transcript string, nested bool) string {
	t.Helper()
	data := Stop(map[string]any{"cwd": repo, "session_id": "stop-todo-session", "transcript_path": transcript, "stop_hook_active": nested}, time.Now())
	if len(data) == 0 {
		return ""
	}
	var output map[string]any
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	return str(output["reason"])
}
