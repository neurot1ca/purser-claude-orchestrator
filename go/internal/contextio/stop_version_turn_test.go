package contextio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStopVersionNoticeSeenInEarlierAssistantRecordOfTurn(t *testing.T) {
	root, home, repo := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PURSER_STATE_DIR", root)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	session := "version-turn"
	statePath := filepath.Join(root, "projects", repositoryProjectKey(repo), "telemetry", "compaction_advisor.json")
	if err := writeAdvisorState(statePath, map[string]any{"session_id": session, "version_pending": map[string]any{"version": "0.1.210"}}); err != nil {
		t.Fatal(err)
	}
	text := func(role, value string) map[string]any {
		return map[string]any{"type": role, "message": map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": value}}}}
	}
	records := []map[string]any{
		text("user", "restart ok devam"),
		text("assistant", "> ⚓ **Purser 0.1.210'a yükseltildi:** ayrıntılar\r\n\r\nDevam ediyorum."),
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "Agent", "input": map[string]any{}}}}},
		{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}}}},
		text("assistant", "Deneme başladı."),
	}
	var transcript strings.Builder
	for _, record := range records {
		line, _ := json.Marshal(record)
		transcript.Write(line)
		transcript.WriteString("\r\n")
	}
	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(transcript.String()), 0600); err != nil {
		t.Fatal(err)
	}
	out := Stop(map[string]any{"cwd": repo, "session_id": session, "transcript_path": transcriptPath, "last_assistant_message": "Deneme başladı."}, time.Now())
	if len(out) != 0 {
		t.Fatalf("Stop re-asked for an already visible update notice: %s", out)
	}
	if state := readAdvisorState(statePath, session); state["version_pending"] != nil {
		t.Fatalf("version_pending was not cleared: %v", state["version_pending"])
	}
}
