package contextio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestoreHandoffRemovesEmptyScaffoldAndKeepsContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "purser-handoff.md")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(map[string]string{"\n": "LF", "\r\n": "CRLF"}[newline], func(t *testing.T) {
			scaffold := strings.Join([]string{
				"# Purser handoff", "## Goal", "User language: <the language the user writes in>", "## State", "## Decisions", "## Next", "## Open risks", "## Files to load",
				stateStart, "Context tokens: 1234 [transcript]", "Triggers: idle [transcript]", stateEnd, "",
			}, newline)
			got := restoreHandoff("compact", path, scaffold, "")
			if got == "" || strings.Contains(got, "<the language the user writes in>") || strings.Contains(got, "## Goal") || strings.Contains(got, "## State") || strings.Contains(got, "## Decisions") || strings.Contains(got, "## Next") || strings.Contains(got, "## Open risks") || strings.Contains(got, "## Files to load") {
				t.Fatalf("restored scaffold contains placeholder or empty sections: %q", got)
			}
			if !strings.Contains(got, "Context tokens: 1234") {
				t.Fatalf("mechanical state was lost: %q", got)
			}

			populated := strings.Join([]string{
				"# Purser handoff", "## Goal", "User language: Türkçe", "Türkçe açıklama 🌍", "## State", "Mechanical state follows.", "## Decisions", "Approved after review.", "## Next", "Should I proceed?", "## Open risks", "## Files to load", stateStart, "Context tokens: 1234", stateEnd, recentStart, "Assistant: Akışı sürdüreyim mi?", recentEnd, "",
			}, newline)
			got = restoreHandoff("compact", path, populated, "")
			for _, want := range []string{"Türkçe açıklama 🌍", "Approved after review.", "Should I proceed?", "Context tokens: 1234", "Assistant: Akışı sürdüreyim mi?"} {
				if !strings.Contains(got, want) {
					t.Errorf("restored handoff omitted %q: %q", want, got)
				}
			}
			if strings.Contains(got, "## Open risks") || strings.Contains(got, "<the language the user writes in>") {
				t.Errorf("restored populated handoff retained scaffold: %q", got)
			}
		})
	}
	if got := restoreHandoff("compact", path, handoffTemplate, ""); got != "" {
		t.Fatalf("empty handoff injected: %q", got)
	}
}

func TestRestoreHandoffStaleCompactKeepsRecentTurnsAndLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "purser-handoff.md")
	if err := os.WriteFile(path, []byte("stale handoff"), 0600); err != nil {
		t.Fatal(err)
	}
	setHandoffModTime(t, path, time.Now().Add(-48*time.Hour))
	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	transcript := "{\"type\":\"user\",\"message\":{\"content\":\"current user turn\"}}\n" +
		"{\"type\":\"assistant\",\"message\":{\"content\":\"current assistant turn\"}}\n" +
		"{\"type\":\"system\",\"subtype\":\"compact_boundary\"}\n"
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0600); err != nil {
		t.Fatal(err)
	}
	content := "# Purser handoff\n## Goal\nUser language: Türkçe\nOld authored goal that must not return.\n## State\nOld state.\n"

	got := restoreHandoff("compact", path, content, transcriptPath)
	for _, want := range []string{
		handoffHeader,
		"Reply to the user in Türkçe; everything else stays as the project rules say.",
		recentStart,
		"current user turn",
		"current assistant turn",
		recentEnd,
		"Full file: " + path,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stale compact handoff omitted %q: %q", want, got)
		}
	}
	if strings.Contains(got, "Old authored goal") || strings.Contains(got, "Old state.") || strings.Contains(got, "## Goal") {
		t.Errorf("stale authored handoff content was injected: %q", got)
	}
}

func TestRestoreHandoffStaleClearReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "purser-handoff.md")
	if err := os.WriteFile(path, []byte("stale handoff"), 0600); err != nil {
		t.Fatal(err)
	}
	setHandoffModTime(t, path, time.Now().Add(-48*time.Hour))
	if got := restoreHandoff("clear", path, "# Purser handoff\n## Goal\nOld goal.\n", ""); got != "" {
		t.Fatalf("stale clear handoff = %q, want empty", got)
	}
}

func TestRestoreHandoffFreshCompactKeepsAuthoredContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "purser-handoff.md")
	if err := os.WriteFile(path, []byte("fresh handoff"), 0600); err != nil {
		t.Fatal(err)
	}
	setHandoffModTime(t, path, time.Now().Add(-time.Hour))
	content := "# Purser handoff\n## Goal\nUser language: Türkçe\nFresh authored goal.\n## State\nFresh state.\n"
	got := restoreHandoff("compact", path, content, "")
	for _, want := range []string{"Fresh authored goal.", "Fresh state.", "Reply to the user in Türkçe; everything else stays as the project rules say."} {
		if !strings.Contains(got, want) {
			t.Errorf("fresh compact handoff omitted %q: %q", want, got)
		}
	}
}

func setHandoffModTime(t *testing.T, path string, modified time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set handoff mtime: %v", err)
	}
}
