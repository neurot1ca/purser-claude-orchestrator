package contextio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompactionAdvicePrecedesAutoCompactThreshold(t *testing.T) {
	for _, test := range []struct {
		name, threshold string
		context         int
		want            bool
	}{
		{"default threshold", "", 220000, true},
		{"configured threshold", "180000", 150000, true},
		{"disabled", "off", 220000, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateRoot := t.TempDir()
			t.Setenv("USERPROFILE", t.TempDir())
			t.Setenv("PURSER_STATE_DIR", stateRoot)
			t.Setenv("PURSER_AUTO_COMPACT_TOKENS", test.threshold)
			if err := os.WriteFile(filepath.Join(stateRoot, "auto-compact.log"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
			writeAdvisorTranscript(t, transcript, []map[string]any{
				{"type": "assistant", "message": map[string]any{"usage": map[string]any{"input_tokens": test.context}}},
			})
			repository := t.TempDir()
			statePath := filepath.Join(stateRoot, "projects", repositoryProjectKey(repository), "telemetry", "compaction_advisor.json")
			got := compactionAdvice(map[string]any{"transcript_path": transcript}, repository, statePath, "auto-headroom")
			advised := strings.Contains(got, "update its Goal, State, Decisions, Next, Open risks, Files to load sections")
			if advised != test.want {
				t.Fatalf("auto-compact headroom advice = %q, want advised=%t", got, test.want)
			}
		})
	}
}

func TestRestoreHandoffFlagsNarrativeUnchangedSincePriorRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "purser-handoff.md")
	content := "# Purser handoff\n## Goal\nOld narrative.\n## State\nCurrent mechanical state.\n## Decisions\nChoice.\n<!-- PURSER:STATE:START -->\nContext tokens: 200000\n<!-- PURSER:STATE:END -->\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	setHandoffModTime(t, path, time.Now().Add(-time.Hour))

	first := restoreHandoff("compact", path, content, "")
	if strings.Contains(first, "Narrative not updated since") {
		t.Fatalf("first restore unexpectedly flagged narrative stale: %q", first)
	}
	if strings.Contains(first, "PURSER:NARRATIVE:") {
		t.Fatalf("first restore leaked narrative marker: %q", first)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second := restoreHandoff("compact", path, string(data), "")
	if !strings.Contains(second, "Narrative not updated since unknown; trust the STATE block, git and NEXT.md over it.") {
		t.Fatalf("unchanged narrative warning missing: %q", second)
	}
	if strings.Contains(second, "PURSER:NARRATIVE:") {
		t.Fatalf("second restore leaked narrative marker: %q", second)
	}
	changed := strings.Replace(string(data), "Old narrative.", "New narrative.", 1)
	if _, date, stale := narrativeRestoreStatus(changed, "2026-11-01"); date != "2026-11-01" || stale {
		t.Fatalf("changed narrative status = date %q, stale %t", date, stale)
	}
	third := restoreHandoff("compact", path, changed, "")
	if strings.Contains(third, "Narrative not updated since") {
		t.Fatalf("changed narrative was flagged stale: %q", third)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if got := restoreHandoff("compact", path, string(updated), ""); !strings.Contains(got, "Narrative not updated since "+today+";") {
		t.Fatalf("unchanged edited narrative did not warn with today's date: %q", got)
	}
	stateOnly := strings.Replace(string(data), "Current mechanical state.", "Updated authored State line.", 1)
	if got := restoreHandoff("compact", path, stateOnly, ""); strings.Contains(got, "Narrative not updated since") {
		t.Fatalf("State-only narrative edit was flagged stale: %q", got)
	}
}

func TestStateRefreshPreservesEmbeddedNarrativeMarker(t *testing.T) {
	for _, writer := range []string{"precompact", "advisor"} {
		t.Run(writer, func(t *testing.T) {
			root, repository := t.TempDir(), t.TempDir()
			t.Setenv("PURSER_STATE_DIR", root)
			if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, output)
			}
			path := filepath.Join(root, "projects", repositoryProjectKey(repository), "telemetry", "purser-handoff.md")
			content := "# Purser handoff\n## Goal\nUnchanged goal.\n" + stateStart + "\nold state\n" + stateEnd + "\n"
			hash := narrativeHash(content)
			marker := "<!-- PURSER:NARRATIVE:" + hash + ":2026-10-30:" + hash + " -->"
			content = strings.Replace(content, stateEnd, marker+"\n"+stateEnd, 1)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.ReplaceAll(content, "\n", "\r\n")), 0600); err != nil {
				t.Fatal(err)
			}
			if writer == "precompact" {
				PreCompact(map[string]any{"cwd": repository})
			} else if err := updateAdvisorHandoff(repository, nil, 123, nil); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), marker) || strings.Index(string(data), marker) < strings.Index(string(data), stateEnd) {
				t.Fatalf("state refresh lost or failed to relocate embedded marker: %s", data)
			}
			if got := restoreHandoff("compact", path, string(data), ""); !strings.Contains(got, "Narrative not updated since 2026-10-30;") {
				t.Fatalf("state refresh suppressed stale narrative warning: %q", got)
			}
		})
	}
}

func TestPreCompactManualTriggerRefreshesStateAndRecentTurns(t *testing.T) {
	for _, source := range []string{"file", "session"} {
		t.Run(source, func(t *testing.T) {
			stateRoot := t.TempDir()
			t.Setenv("PURSER_STATE_DIR", stateRoot)
			repository := t.TempDir()
			command := exec.Command("git", "init", "--quiet", repository)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, output)
			}
			handoff := filepath.Join(stateRoot, "projects", repositoryProjectKey(repository), "telemetry", "purser-handoff.md")
			content := "# Purser handoff\n## Goal\nCurrent goal.\n## State\n## Decisions\n## Next\n## Open risks\n## Files to load\n" + stateStart + "\nold state\n" + stateEnd + "\n" + recentStart + "\nold recent\n" + recentEnd + "\n"
			hash := narrativeHash(content)
			content += "<!-- PURSER:NARRATIVE:" + hash + ":2026-10-30:" + hash + " -->\n"
			if err := os.MkdirAll(filepath.Dir(handoff), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(handoff, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			setHandoffModTime(t, handoff, time.Now().Add(-time.Hour))
			transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
			writeAdvisorTranscript(t, transcript, []map[string]any{
				{"type": "user", "message": map[string]any{"content": "current user"}},
				{"type": "assistant", "message": map[string]any{"content": "current assistant", "usage": map[string]any{"input_tokens": 123456}}},
			})
			event := map[string]any{"cwd": repository, "trigger": "manual", "transcript_path": transcript}
			if source == "session" {
				event["transcript_path"] = ""
				var records []any
				for _, record := range readTranscriptRecords(transcript) {
					records = append(records, record)
				}
				event["transcript_records"] = records
			}
			PreCompact(event)
			updated, err := os.ReadFile(handoff)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(handoff)
			if err != nil {
				t.Fatal(err)
			}
			if !info.ModTime().After(time.Now().Add(-time.Minute)) {
				t.Fatalf("manual PreCompact did not refresh handoff mtime: %v", info.ModTime())
			}
			for _, want := range []string{"Context tokens: 123456", recentStart, "current user", "current assistant", recentEnd} {
				if !strings.Contains(string(updated), want) {
					t.Errorf("manual PreCompact output omitted %q: %s", want, updated)
				}
			}
			if !strings.Contains(string(updated), "PURSER:NARRATIVE:") || !strings.Contains(string(updated), ":2026-10-30:") {
				t.Fatalf("PreCompact state refresh did not preserve narrative marker and date: %s", updated)
			}
			if strings.Contains(string(updated), "old recent") || strings.Contains(string(updated), "old state") {
				t.Fatalf("manual PreCompact left old mechanical blocks: %s", updated)
			}
		})
	}
}
