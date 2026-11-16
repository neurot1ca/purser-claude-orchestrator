package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/claudework"
	"purser-hook/internal/contextio"
)

func TestClaudeWorkLifecycleCaptureAndReplay(t *testing.T) {
	root := claudeWorkTempDir(t)
	t.Setenv("PURSER_STATE_DIR", filepath.Join(root, "state"))
	project := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(project, "s1.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"assistant","sessionId":"s1","timestamp":"2026-11-14T10:00:00Z","message":{"id":"m1","role":"assistant","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":1},"content":[{"type":"text","text":"latest excerpt"}]}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	event := map[string]any{"hook_event_name": "SessionStart", "session_id": "s1", "cwd": project, "transcript_path": transcript, "model": "claude-sonnet"}
	if err := captureClaudeWork(event); err != nil {
		t.Fatal(err)
	}
	event["hook_event_name"], event["last_assistant_message"] = "Stop", "latest excerpt"
	delete(event, "model")
	if err := captureClaudeWork(event); err != nil {
		t.Fatal(err)
	}
	if err := captureClaudeWork(event); err != nil {
		t.Fatal(err)
	}
	// The repository key is derived from the actual Git root.
	files, err := filepath.Glob(filepath.Join(root, "state", "claude_work", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("records=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var record claudework.Record
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.State != "response_finished" || record.FinalExcerpt != "latest excerpt" || record.StartedAt == "" || record.Model != "claude-sonnet" || record.DurationMS == nil {
		t.Fatalf("record=%+v", record)
	}
	rows, err := filepath.Glob(filepath.Join(root, "state", "telemetry", "events", "????-??", "session_s1.jsonl"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("telemetry=%v err=%v", rows, err)
	}
	data, err = os.ReadFile(rows[0])
	if err != nil || strings.Count(string(data), "CLAUDE_WORK_USAGE_RECORDED") != 1 {
		t.Fatalf("duplicate or missing telemetry: %s err=%v", data, err)
	}
	var eventRecord map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &eventRecord); err != nil {
		t.Fatal(err)
	}
	payload := eventRecord["payload"].(map[string]any)
	workID := claudework.Identity("session", record.SessionID, nil)
	if payload["model"] != "claude-sonnet" || payload["input_tokens"] != float64(10) || payload["work_id"] != workID || payload["state"] != "response_finished" {
		t.Fatalf("payload=%#v", payload)
	}
	turn, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = turn.WriteString(`{"type":"assistant","sessionId":"s1","message":{"id":"m2","role":"assistant","usage":{"input_tokens":5,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"content":[{"type":"text","text":"new response"}]}}` + "\n")
	closeErr := turn.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("append transcript: %v, %v", err, closeErr)
	}
	if err := captureClaudeWork(event); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("new Stop response did not append telemetry: %s", data)
	}
	if err := json.Unmarshal(lines[1], &eventRecord); err != nil {
		t.Fatal(err)
	}
	payload = eventRecord["payload"].(map[string]any)
	if payload["work_id"] != workID || payload["state"] != "response_finished" || payload["input_tokens"] != float64(15) || payload["output_tokens"] != float64(3) || payload["cache_read_input_tokens"] != float64(3) || payload["cache_creation_input_tokens"] != float64(1) {
		t.Fatalf("new cumulative totals=%#v", payload)
	}
	event["hook_event_name"], event["reason"] = "SessionEnd", "exit"
	if err := captureClaudeWork(event); err != nil {
		t.Fatal(err)
	}
	if err := captureClaudeWork(event); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &record); err != nil || record.State != "session_ended" || record.FinalExcerpt != "new response" {
		t.Fatalf("SessionEnd did not preserve the record: %+v err=%v", record, err)
	}
	data, err = os.ReadFile(rows[0])
	if err != nil || strings.Count(string(data), "CLAUDE_WORK_USAGE_RECORDED") != 3 {
		t.Fatalf("SessionEnd telemetry replay duplicated or missing: %s err=%v", data, err)
	}
	lines = bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if err := json.Unmarshal(lines[2], &eventRecord); err != nil {
		t.Fatal(err)
	}
	payload = eventRecord["payload"].(map[string]any)
	if payload["work_id"] != workID || payload["state"] != "session_ended" || payload["input_tokens"] != float64(15) {
		t.Fatalf("SessionEnd cumulative totals=%#v", payload)
	}
}

func TestClaudeWorkSubagentUsesAgentTranscript(t *testing.T) {
	root := claudeWorkTempDir(t)
	t.Setenv("PURSER_STATE_DIR", filepath.Join(root, "state"))
	project := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	subagents := filepath.Join(project, "subagents")
	if err := os.MkdirAll(subagents, 0700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(subagents, "agent-agent-1.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"assistant","sessionId":"s3","agentId":"agent-1","isSidechain":true,"message":{"id":"m3","role":"assistant","content":[{"type":"text","text":"agent result"}]}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(project, "parent.jsonl")
	if err := os.WriteFile(parent, []byte(`{"type":"assistant","sessionId":"s3","message":{"id":"m4","role":"assistant","content":[{"type":"text","text":"parent result"}]}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start := map[string]any{"hook_event_name": "SubagentStart", "session_id": "s3", "cwd": project, "agent_id": "agent-1", "agent_type": "general-purpose"}
	stop := map[string]any{"hook_event_name": "SubagentStop", "session_id": "s3", "cwd": project, "agent_id": "agent-1", "agent_transcript_path": transcript, "transcript_path": parent}
	if err := captureClaudeWork(start); err != nil {
		t.Fatal(err)
	}
	if err := captureClaudeWork(stop); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "state", "claude_work", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("records=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var record claudework.Record
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.State != "response_finished" || record.FinalExcerpt != "agent result" || record.AgentType == nil || *record.AgentType != "general-purpose" {
		t.Fatalf("record=%+v", record)
	}
	events, err := filepath.Glob(filepath.Join(root, "state", "telemetry", "events", "????-??", "session_s3.jsonl"))
	if err != nil || len(events) != 1 {
		t.Fatalf("subagent telemetry=%v err=%v", events, err)
	}
	telemetry, err := os.ReadFile(events[0])
	if err != nil || !strings.Contains(string(telemetry), `"event_type":"CLAUDE_WORK_USAGE_RECORDED"`) {
		t.Fatalf("real subagent telemetry missing: %s err=%v", telemetry, err)
	}
}

func TestClaudeWorkSkipsPhantomSubagent(t *testing.T) {
	root := claudeWorkTempDir(t)
	t.Setenv("PURSER_STATE_DIR", filepath.Join(root, "state"))
	project := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	event := map[string]any{"hook_event_name": "SubagentStop", "session_id": "phantom-session", "cwd": project, "agent_id": "phantom", "agent_transcript_path": filepath.Join(root, "session", "subagents", "agent-phantom.jsonl")}
	if err := captureClaudeWork(event); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "state", "claude_work", "*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("phantom subagent records=%v err=%v", files, err)
	}
}

func TestClaudeWorkSessionEndPreservesExcerptAndSchedulesCleanup(t *testing.T) {
	root := claudeWorkTempDir(t)
	t.Setenv("PURSER_STATE_DIR", filepath.Join(root, "state"))
	project := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	session := "s2"
	prior := claudework.Record{ProjectKey: contextio.ProjectKey(project), Kind: "session", SessionID: &session, State: "response_finished", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), MetadataStatus: "partial", FinalExcerpt: "latest excerpt"}
	if err := claudework.Update(filepath.Join(root, "state"), prior); err != nil {
		t.Fatal(err)
	}
	event := map[string]any{"hook_event_name": "SessionEnd", "session_id": "s2", "cwd": project, "reason": "exit"}
	input, err := os.CreateTemp(root, "event-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(input).Encode(event); err != nil {
		t.Fatal(err)
	}
	if _, err = input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	oldStdin, oldArgs, oldCleanup, cleaned := os.Stdin, os.Args, requestCleanup, false
	os.Stdin, requestCleanup = input, func(string) error { cleaned = true; return nil }
	os.Args = []string{"purser-hook", "claude-work"}
	t.Cleanup(func() { os.Stdin, os.Args, requestCleanup = oldStdin, oldArgs, oldCleanup; _ = input.Close() })
	if code := claudeWorkMain(); code != 0 {
		t.Fatalf("claudeWorkMain=%d", code)
	}
	if !cleaned {
		t.Fatal("SessionEnd did not enqueue cleanup")
	}
	files, err := filepath.Glob(filepath.Join(root, "state", "claude_work", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("records=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var record claudework.Record
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.State != "session_ended" || record.FinalExcerpt != "latest excerpt" {
		t.Fatalf("record=%+v", record)
	}
}

func TestClaudeWorkStopReconcilesInterruptedSubagent(t *testing.T) {
	root := claudeWorkTempDir(t)
	state := filepath.Join(root, "state")
	t.Setenv("PURSER_STATE_DIR", state)
	project := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	session, agent := "session-interrupt", "agent-interrupt"
	parent := filepath.Join(project, session+".jsonl")
	path := filepath.Join(project, session, "subagents", "agent-"+agent+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	record := claudework.Record{ProjectKey: contextio.ProjectKey(project), Kind: "subagent", SessionID: &session, AgentID: &agent, State: "running", MetadataStatus: "partial", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), TranscriptPath: parent}
	if err := claudework.Update(state, record); err != nil {
		t.Fatal(err)
	}
	if err := captureClaudeWork(map[string]any{"hook_event_name": "Stop", "session_id": session, "cwd": project, "transcript_path": parent}); err != nil {
		t.Fatal(err)
	}
	storedPath, _ := claudework.Path(state, record.ProjectKey, record)
	data, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.State != "interrupted" || record.EndedAt == "" {
		t.Fatalf("subagent not reconciled at Stop: %+v", record)
	}
}

func claudeWorkTempDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.Abs(filepath.Join("..", "..", "..", ".tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "claude-work-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}
