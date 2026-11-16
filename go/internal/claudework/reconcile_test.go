package claudework

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReconcileRunningSubagents(t *testing.T) {
	now := time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, content, meta, sessionState, want   string
		parent, missing, parentPath, otherProject bool
		idle                                      time.Duration
	}{
		{name: "large interrupt text blocks", content: `{"type":"user","sessionId":"session-1","agentId":"agent-1","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`, parentPath: true, parent: true, want: "interrupted"},
		{name: "interrupt string", content: `{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"}}`, want: "interrupted"},
		{name: "later response", content: `{"type":"user","message":{"content":"[Request interrupted by user]"}}` + "\n" + `{"type":"assistant","message":{"role":"assistant","content":"resumed"}}`, want: "running"},
		{name: "partial last line", content: `{"type":"user","message":{"content":"[Request interrupted by user]"}}` + "\n" + `{"type":`, want: "running"},
		{name: "wrong identity", content: `{"type":"user","sessionId":"other","message":{"content":"[Request interrupted by user]"}}`, want: "running"},
		{name: "assistant quote", content: `{"type":"assistant","message":{"role":"assistant","content":"[Request interrupted by user]"}}`, want: "running"},
		{name: "missing transcript finished meta", missing: true, meta: `{"finished":true}`, want: "finished"},
		{name: "oversized transcript finished meta", content: `{"type":"assistant"}`, meta: `{"finished":true}`, want: "finished"},
		{name: "malformed meta", missing: true, meta: `{"finished":`, want: "running"},
		{name: "missing transcript ended session", missing: true, sessionState: "session_ended", want: "session_ended"},
		{name: "oversized transcript ended session", content: `{"type":"assistant"}`, sessionState: "session_ended", want: "session_ended"},
		{name: "response finished parent still live", sessionState: "response_finished", want: "running"},
		{name: "idle parent present", parent: true, idle: 2 * time.Hour, want: "running"},
		{name: "idle parent missing", idle: 30 * time.Minute, want: "session_ended"},
		{name: "recent parent missing", idle: 29 * time.Minute, want: "running"},
		{name: "other project", content: `{"type":"user","message":{"content":"[Request interrupted by user]"}}`, otherProject: true, want: "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := filepath.Abs(filepath.Join("..", "..", "..", ".tmp"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.MkdirTemp(base, "reconcile-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			session, agent := "session-1", "agent-1"
			parent := filepath.Join(root, session+".jsonl")
			path := filepath.Join(root, session, "subagents", "agent-"+agent+".jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			mtime := now.Add(-tc.idle)
			if !tc.missing {
				// A 150 KB first line ensures terminal detection uses the tail, not a whole-file cap.
				data := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 150000) + `"}}` + "\n" + tc.content + "\n\n"
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, mtime, mtime); err != nil {
					t.Fatal(err)
				}
			}
			if tc.parent {
				if err := os.WriteFile(parent, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.meta != "" {
				metaPath := strings.TrimSuffix(path, ".jsonl") + ".meta.json"
				if err := os.WriteFile(metaPath, []byte(tc.meta), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(metaPath, mtime, mtime); err != nil {
					t.Fatal(err)
				}
			}
			record := Record{ProjectKey: "project", Kind: "subagent", SessionID: &session, AgentID: &agent, State: "running", MetadataStatus: "partial", ObservedAt: now.Format(time.RFC3339), StartedAt: now.Add(-3 * time.Hour).Format(time.RFC3339), TranscriptPath: path}
			if tc.parentPath {
				record.TranscriptPath = parent
			}
			if tc.otherProject {
				record.ProjectKey = "other"
			}
			if err := Update(root, record); err != nil {
				t.Fatal(err)
			}
			if tc.sessionState != "" {
				parentRecord := Record{ProjectKey: "project", Kind: "session", SessionID: &session, State: tc.sessionState, MetadataStatus: "partial", ObservedAt: now.Format(time.RFC3339), EndedAt: now.Add(-time.Minute).Format(time.RFC3339)}
				if err := Update(root, parentRecord); err != nil {
					t.Fatal(err)
				}
			}
			Reconcile(root, "project", now)
			storedPath, _ := Path(root, record.ProjectKey, record)
			data, err := os.ReadFile(storedPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record.State != tc.want {
				t.Fatalf("state=%s want=%s", record.State, tc.want)
			}
			if tc.want != "running" {
				end := mtime
				if tc.sessionState == "session_ended" {
					end = now.Add(-time.Minute)
				}
				if record.EndedAt != end.Format(time.RFC3339Nano) || record.DurationMS == nil || *record.DurationMS != end.Sub(now.Add(-3*time.Hour)).Milliseconds() {
					t.Fatalf("terminal timing: %+v", record)
				}
				Reconcile(root, "project", now.Add(time.Hour))
				replayed, err := os.ReadFile(storedPath)
				if err != nil || string(replayed) != string(data) {
					t.Fatalf("terminal record changed on replay: %s, %v", replayed, err)
				}
			}
		})
	}
}
