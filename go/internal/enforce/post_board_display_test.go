package enforce

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/contextio"
)

func TestPostBoardCountsOnlyConfirmedFinishedDisplays(t *testing.T) {
	root := preTempDir(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PURSER_STATE_DIR", root)
	contextio.RecordUserLanguage(root, "Turkish")
	project := contextio.ProjectKey(repo)
	event := map[string]any{"cwd": repo, "session_id": "board-display", "stop_hook_active": true,
		"transcript_path": filepath.Join(root, "transcript.jsonl")}
	id, activeID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, job := range []map[string]any{
		{"jobId": id, "state": "SUCCEEDED", "label": "Finished", "boardDisplayCount": 0, "finishedAt": time.Now().Unix()},
		{"jobId": activeID, "state": "RUNNING", "label": "Active", "boardDisplayCount": 0},
	} {
		job["repoRoot"], job["call"] = repo, "call"
		jobID := stringValue(job["jobId"])
		if err := atomicAdmissionJSON(filepath.Join(root, "codex_exec_mcp", "jobs", jobID+".json"), job); err != nil {
			t.Fatal(err)
		}
		if err := atomicAdmissionJSON(filepath.Join(root, "async_jobs", jobID+".json"), map[string]any{
			"jobId": jobID, "call": "call", "project_key": project,
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want int) {
		t.Helper()
		job := readBoardJSON(filepath.Join(root, "codex_exec_mcp", "jobs", id+".json"))
		if got := int(boardNumber(job["boardDisplayCount"])); got != want {
			t.Fatalf("display count = %d, want %d", got, want)
		}
	}
	for display := 1; display <= 2; display++ {
		// A new genuine user message identifies a new display turn.
		transcript := strings.Repeat("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"Next\"}}\n", display)
		if err := os.WriteFile(stringValue(event["transcript_path"]), []byte(transcript), 0600); err != nil {
			t.Fatal(err)
		}
		queuePostReceipt(root, event, repo, "Dispatch receipt")
		board := postProjectJobBoard(root, event, repo, project)
		if !strings.Contains(board, "Finished") {
			t.Fatal("finished job missing from board")
		}
		pending := mapValue(readPostReceiptState(postReceiptStatePath(root, repo), "board-display")["dispatch_pending"])
		ids, _ := pending["board_job_ids"].([]any)
		if len(ids) != 1 || ids[0] != id {
			t.Fatalf("finished board IDs = %v", ids)
		}
		event["last_assistant_message"] = "No board shown."
		contextio.Stop(event, time.Now())
		check(display - 1)
		visible, _, _ := strings.Cut(board, "\nReply to the user in ")
		event["last_assistant_message"] = visible
		contextio.Stop(event, time.Now())
		check(display)
		// A missing receipt keeps the board pending, without recounting it.
		event["last_assistant_message"] = visible + "\nDispatch receipt"
		contextio.Stop(event, time.Now())
		check(display)
		// Recreate pending state, as after a failed state write or cached receipt.
		if err := atomicAdmissionJSON(postReceiptStatePath(root, repo), map[string]any{
			"session_id": "board-display", "dispatch_pending": pending,
		}); err != nil {
			t.Fatal(err)
		}
		contextio.Stop(event, time.Now())
		check(display)
	}
	board := postProjectJobBoard(root, event, repo, project)
	if strings.Contains(board, "Finished") || !strings.Contains(board, "Active") {
		t.Fatalf("board after two displays = %q", board)
	}
	active := readBoardJSON(filepath.Join(root, "codex_exec_mcp", "jobs", activeID+".json"))
	if boardNumber(active["boardDisplayCount"]) != 0 {
		t.Fatal("active job counted as a finished display")
	}
}
