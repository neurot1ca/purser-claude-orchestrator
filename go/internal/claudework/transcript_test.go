package claudework

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func transcriptRecord(t *testing.T, kind string) Record {
	t.Helper()
	session, agentValue := "session-1", "agent-1"
	var agent *string
	root := t.TempDir()
	dir, name := root, session+".jsonl"
	if kind == "subagent" {
		dir, name = filepath.Join(root, "subagents"), agentValue+".jsonl"
		agent = &agentValue
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return Record{Kind: kind, SessionID: &session, AgentID: agent, TranscriptPath: filepath.Join(dir, name), MetadataStatus: "partial"}
}

func line(t *testing.T, value any) string {
	t.Helper()
	b, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestExtractTranscriptDeduplicatesAndExcludesSidechains(t *testing.T) {
	r := transcriptRecord(t, "session")
	usage := map[string]any{"input_tokens": uint64(11), "output_tokens": uint64(3), "cache_read_input_tokens": uint64(4), "cache_creation_input_tokens": uint64(2)}
	assistant := map[string]any{"type": "assistant", "sessionId": "session-1", "timestamp": "2026-01-02T03:04:05Z", "message": map[string]any{"id": "m1", "role": "assistant", "model": "claude-test", "usage": usage, "content": []any{map[string]any{"type": "thinking", "thinking": "secret"}, map[string]any{"type": "tool_use", "name": "secret"}, map[string]any{"type": "text", "text": "Done `safely`\n now."}}}}
	nested := map[string]any{"type": "assistant", "sessionId": "session-1", "message": map[string]any{"id": "m1", "role": "assistant", "message": map[string]any{"usage": usage}}}
	side := map[string]any{"type": "assistant", "isSidechain": true, "sessionId": "session-1", "message": map[string]any{"id": "child", "role": "assistant", "usage": usage, "content": []any{map[string]any{"type": "text", "text": "child"}}}}
	data := line(t, assistant) + "\r\n" + line(t, nested) + "\r\n" + line(t, side) + "\r\n"
	if e := os.WriteFile(r.TranscriptPath, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	ExtractTranscript(&r)
	if r.Usage == nil || r.Usage.InputTokens == nil || *r.Usage.InputTokens != 11 || *r.Usage.OutputTokens != 3 || r.Usage.Completeness != "complete" {
		t.Fatalf("unexpected usage: %+v", r.Usage)
	}
	if strings.Contains(r.FinalExcerpt, "secret") || strings.ContainsAny(r.FinalExcerpt, "\r\n") || !strings.Contains(r.FinalExcerpt, `\`+"`"+`safely`) {
		t.Fatalf("unsafe excerpt: %q", r.FinalExcerpt)
	}
	if r.Model != "claude-test" || r.LastResponseAt == "" {
		t.Fatalf("metadata not captured: %+v", r)
	}
}

func TestExtractTranscriptConflictAndIncompleteInputUnknown(t *testing.T) {
	r := transcriptRecord(t, "session")
	a := map[string]any{"type": "assistant", "message": map[string]any{"id": "m1", "role": "assistant", "usage": map[string]any{"input_tokens": 1, "output_tokens": 2, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 4}}}
	b := map[string]any{"type": "assistant", "message": map[string]any{"id": "m1", "role": "assistant", "usage": map[string]any{"input_tokens": 2, "output_tokens": 2, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 4}}}
	if e := os.WriteFile(r.TranscriptPath, []byte(line(t, a)+"\n"+line(t, b)+"\n"+`{"type":"assistant"`), 0600); e != nil {
		t.Fatal(e)
	}
	ExtractTranscript(&r)
	if r.Usage == nil || r.Usage.Completeness != "unknown" || r.Usage.InputTokens != nil || r.MetadataStatus != "partial" {
		t.Fatalf("conflicting/truncated input was trusted: %+v", r.Usage)
	}
}

func TestExtractSubagentAndRejectPathMismatch(t *testing.T) {
	r := transcriptRecord(t, "subagent")
	event := map[string]any{"type": "assistant", "sidechain": true, "sessionId": "session-1", "agentId": "agent-1", "message": map[string]any{"id": "a1", "role": "assistant", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}}}
	if e := os.WriteFile(r.TranscriptPath, []byte(line(t, event)+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	ExtractTranscript(&r)
	if r.Usage == nil || r.Usage.InputTokens == nil || *r.Usage.InputTokens != 1 {
		t.Fatalf("sidechain usage missing: %+v", r.Usage)
	}
	r.TranscriptPath = filepath.Join(filepath.Dir(r.TranscriptPath), "other.jsonl")
	ExtractTranscript(&r)
	if r.Usage == nil || r.Usage.Completeness != "unknown" {
		t.Fatalf("mismatched path accepted: %+v", r.Usage)
	}
}

func TestExtractTranscriptMissingAndSymlink(t *testing.T) {
	r := transcriptRecord(t, "session")
	ExtractTranscript(&r)
	if r.Usage == nil || r.Usage.Completeness != "unknown" || r.MetadataStatus != "partial" {
		t.Fatalf("missing transcript did not yield a partial observation: %+v", r)
	}
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, r.TranscriptPath); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	ExtractTranscript(&r)
	if r.Usage == nil || r.Usage.Completeness != "unknown" {
		t.Fatalf("symlink transcript was accepted: %+v", r.Usage)
	}
}

func TestExtractSubagentRejectsDifferentSession(t *testing.T) {
	for _, key := range []string{"sessionId", "session_id"} {
		r := transcriptRecord(t, "subagent")
		event := map[string]any{"type": "assistant", "sidechain": true, key: "other-session", "agentId": "agent-1", "timestamp": "2026-01-02T03:04:05Z", "message": map[string]any{"id": "a1", "role": "assistant", "model": "wrong-session-model", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}, "content": []any{map[string]any{"type": "text", "text": "wrong-session-text"}}}}
		if err := os.WriteFile(r.TranscriptPath, []byte(line(t, event)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		ExtractTranscript(&r)
		if r.Usage == nil || r.Usage.Completeness != "unknown" || r.Usage.InputTokens != nil || r.Usage.OutputTokens != nil || r.Usage.CacheReadInputTokens != nil || r.Usage.CacheCreationInputTokens != nil || r.Model != "" || r.ModelSource != "" || r.FinalExcerpt != "" || r.StartedAt != "" || r.LastResponseAt != "" {
			t.Fatalf("%s mismatch contributed usage or metadata: %+v", key, r)
		}
	}
}

func TestExtractTranscriptUsageOverflowUnknown(t *testing.T) {
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		r := transcriptRecord(t, "session")
		var data strings.Builder
		for i, n := range []uint64{^uint64(0), 1} {
			usage := map[string]any{"input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}
			usage[key] = n
			data.WriteString(line(t, map[string]any{"type": "assistant", "message": map[string]any{"id": []string{"m1", "m2"}[i], "usage": usage}}) + "\n")
		}
		if err := os.WriteFile(r.TranscriptPath, []byte(data.String()), 0600); err != nil {
			t.Fatal(err)
		}
		ExtractTranscript(&r)
		if r.Usage == nil || r.Usage.Completeness != "unknown" || r.Usage.UnknownReason != "usage_overflow" || r.Usage.InputTokens != nil || r.Usage.OutputTokens != nil || r.Usage.CacheReadInputTokens != nil || r.Usage.CacheCreationInputTokens != nil || r.MetadataStatus != "partial" {
			t.Fatalf("%s overflow was trusted: %+v", key, r.Usage)
		}
	}
}

func TestCaptureHookFieldsAllowlist(t *testing.T) {
	r := CaptureHookFields(Record{}, map[string]any{"session_id": "s", "agent_id": "a", "agent_type": "worker", "transcript_path": "x.jsonl", "model": "m", "prompt": "must not persist"})
	if r.SessionID == nil || *r.SessionID != "s" || r.AgentID == nil || *r.AgentID != "a" || r.TranscriptPath != "x.jsonl" || strings.Contains(r.FinalExcerpt, "prompt") {
		t.Fatalf("hook normalization failed: %+v", r)
	}
}
