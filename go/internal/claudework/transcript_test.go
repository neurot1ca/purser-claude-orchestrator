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

func TestExtractTranscriptAcceptsResolvedParentPath(t *testing.T) {
	r := transcriptRecord(t, "session")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(r.TranscriptPath), alias); err != nil {
		t.Skipf("directory symlink creation unavailable: %v", err)
	}
	r.TranscriptPath = filepath.Join(alias, filepath.Base(r.TranscriptPath))
	usage := map[string]any{"input_tokens": 1, "output_tokens": 2, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}
	data := line(t, map[string]any{"type": "assistant", "message": map[string]any{"id": "m1", "usage": usage}}) + "\n"
	if err := os.WriteFile(r.TranscriptPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	ExtractTranscript(&r)
	if r.Usage == nil || r.Usage.InputTokens == nil || *r.Usage.InputTokens != 1 {
		t.Fatalf("resolved parent spelling was rejected: %+v", r.Usage)
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

func TestCaptureHookEffortShapes(t *testing.T) {
	tests := []struct {
		name  string
		event map[string]any
		want  string
	}{
		{name: "string", event: map[string]any{"effort": "high"}, want: "high"},
		{name: "level object", event: map[string]any{"effort": map[string]any{"level": "high"}}, want: "high"},
		{name: "value object", event: map[string]any{"reasoning_effort": map[string]any{"type": "reasoning", "value": "medium"}}, want: "medium"},
		{name: "unexpected object", event: map[string]any{"effort": map[string]any{"type": "reasoning", "name": "high"}}},
		{name: "ambiguous object", event: map[string]any{"effort": map[string]any{"level": "high", "value": "medium"}}},
		{name: "number", event: map[string]any{"effort": 3}},
		{name: "null", event: map[string]any{"effort": nil}},
		{name: "missing", event: map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := CaptureHookFields(Record{}, tt.event)
			if r.Effort != tt.want {
				t.Fatalf("effort = %q, want %q", r.Effort, tt.want)
			}
		})
	}
}

func TestCaptureAgentMetaPresentAbsentAndMalformed(t *testing.T) {
	for _, tt := range []struct {
		name       string
		meta       string
		wantType   string
		wantModel  string
		wantState  string
		wantExists bool
	}{
		{name: "present", meta: `{"agentType":"purser:helper","pickedEffortModel":"claude-opus-5-5","finished":true}`, wantType: "purser:helper", wantModel: "claude-opus-5-5", wantState: "finished", wantExists: true},
		{name: "absent"},
		{name: "malformed", meta: `{bad`, wantExists: true},
		{name: "oversized", meta: strings.Repeat("x", agentMetaLimit+1), wantExists: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "session", "subagents")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(dir, "agent-a6a549c6acfa70263.jsonl")
			if tt.name == "present" || tt.name == "absent" {
				if err := os.WriteFile(transcript, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.meta != "" {
				if err := os.WriteFile(filepath.Join(dir, "agent-a6a549c6acfa70263.meta.json"), []byte(tt.meta), 0600); err != nil {
					t.Fatal(err)
				}
			}
			agentID := "a6a549c6acfa70263"
			record := Record{Kind: "subagent", AgentID: &agentID, State: "response_finished", TranscriptPath: transcript}
			transcriptPresent, metaPresent := CaptureAgentMeta(&record)
			if transcriptPresent != (tt.name == "present" || tt.name == "absent") || metaPresent != tt.wantExists {
				t.Fatalf("presence = %v/%v, want %v/%v", transcriptPresent, metaPresent, tt.name == "present" || tt.name == "absent", tt.wantExists)
			}
			gotType := ""
			if record.AgentType != nil {
				gotType = *record.AgentType
			}
			if gotType != tt.wantType || record.Model != tt.wantModel || record.State != map[bool]string{true: "finished", false: "response_finished"}[tt.wantState == "finished"] {
				t.Fatalf("metadata = type %q model %q state %q", gotType, record.Model, record.State)
			}
		})
	}
}

func TestCaptureDispatcherFirstPrompt(t *testing.T) {
	for _, tc := range []struct {
		name, prompt, later, want            string
		blocks, mismatch, badPath, malformed bool
	}{
		{name: "CRLF string", prompt: "label: Fix API\r\nORCH|task=api-fix|phase=implementation\r\nWork", want: "Fix API"},
		{name: "text blocks and unicode bound", prompt: "label: " + strings.Repeat("雪", 100), blocks: true, want: strings.Repeat("雪", 80)},
		{name: "task fallback", prompt: "ORCH|task=api-fix|phase=implementation", want: "api-fix"},
		{name: "empty label fallback", prompt: "label: \nORCH|task=api-fix|phase=plan", want: "api-fix"},
		{name: "never later prompt", prompt: "Start", later: "label: wrong\nORCH|task=wrong"},
		{name: "ambiguous label", prompt: "label: one\nlabel: two\nORCH|task=api-fix"},
		{name: "ambiguous task", prompt: "label: one\nORCH|task=one\nORCH|task=two"},
		{name: "unsafe label", prompt: "label: unsafe\x00label\nORCH|task=api-fix"},
		{name: "identity mismatch", prompt: "label: wrong", mismatch: true},
		{name: "path mismatch", prompt: "label: wrong", badPath: true},
		{name: "malformed before prompt", prompt: "label: wrong", malformed: true},
		{name: "bounded read", prompt: strings.Repeat("x", transcriptLimit) + "\nlabel: wrong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := transcriptRecord(t, "subagent")
			agentType := "purser:dispatcher"
			r.AgentType = &agentType
			// Exercise Claude's agent- prefix and portable slash paths on Windows too.
			r.TranscriptPath = filepath.ToSlash(filepath.Join(filepath.Dir(r.TranscriptPath), "agent-"+*r.AgentID+".jsonl"))
			if tc.badPath {
				r.TranscriptPath = filepath.Join(filepath.Dir(r.TranscriptPath), "wrong.jsonl")
			}
			user := func(prompt string) string {
				var content any = prompt
				if tc.blocks {
					content = []any{map[string]any{"type": "text", "text": prompt}}
				}
				session := *r.SessionID
				if tc.mismatch {
					session = "another-session"
				}
				return line(t, map[string]any{"type": "user", "sessionId": session, "agentId": *r.AgentID,
					"isSidechain": true, "message": map[string]any{"role": "user", "content": content}}) + "\r\n"
			}
			data := user(tc.prompt)
			if tc.malformed {
				data = "{bad json}\n" + data
			}
			if tc.later != "" {
				data += user(tc.later)
			}
			if err := os.WriteFile(r.TranscriptPath, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			CaptureAgentMeta(&r)
			if r.TaskLabel != tc.want {
				t.Fatalf("label = %q, want %q", r.TaskLabel, tc.want)
			}
		})
	}
}

func TestDispatcherLabelSurvivesTerminalMerge(t *testing.T) {
	r := sampleRecord()
	r.TaskLabel = "Fix API"
	root := t.TempDir()
	if err := Update(root, r); err != nil {
		t.Fatal(err)
	}
	r.TaskLabel = ""
	r.State = "response_finished"
	merged, err := MergeExisting(root, r)
	if err != nil || merged.TaskLabel != "Fix API" {
		t.Fatalf("terminal label lost: %+v, %v", merged, err)
	}
	r.TaskLabel = strings.Repeat("x", 241)
	if err := Update(root, r); err == nil {
		t.Fatal("oversized label accepted")
	}
}

func TestExtractTranscriptEffortShapesKeepUsage(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value any
		want  string
		omit  bool
	}{
		{name: "string", key: "effort", value: "high", want: "high"},
		{name: "level object", key: "effort", value: map[string]any{"level": "high"}, want: "high"},
		{name: "value object", key: "reasoning_effort", value: map[string]any{"type": "reasoning", "value": "medium"}, want: "medium"},
		{name: "unexpected object", key: "effort", value: map[string]any{"type": "reasoning", "name": "high"}},
		{name: "number", key: "effort", value: 3},
		{name: "null", key: "effort", value: nil},
		{name: "missing", omit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := transcriptRecord(t, "session")
			message := map[string]any{"id": "m1", "role": "assistant", "usage": map[string]any{"input_tokens": 11, "output_tokens": 3, "cache_read_input_tokens": 4, "cache_creation_input_tokens": 2}, "content": []any{map[string]any{"type": "text", "text": "still recorded"}}}
			event := map[string]any{"type": "assistant", "message": message}
			if !tt.omit {
				message[tt.key] = tt.value
			}
			if err := os.WriteFile(r.TranscriptPath, []byte(line(t, event)+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ExtractTranscript(&r)
			if r.Effort != tt.want {
				t.Fatalf("effort = %q, want %q", r.Effort, tt.want)
			}
			if r.Usage == nil || r.Usage.Completeness != "complete" || r.Usage.InputTokens == nil || *r.Usage.InputTokens != 11 || r.FinalExcerpt == "" {
				t.Fatalf("effort shape dropped transcript metadata or usage: %+v", r)
			}
		})
	}
}
