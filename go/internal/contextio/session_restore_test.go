package contextio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
