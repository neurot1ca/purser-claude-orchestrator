package contextio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/testutil"
)

func TestFreshSessionNudgeThresholdAndStretch(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	statePath := filepath.Join(t.TempDir(), "telemetry", "compaction_advisor.json")
	const session = "fresh-session-stretch"

	if got := freshSessionNudge(freshNudgeRecords(freshSessionNudgeTokens-1), statePath, session); got != "" {
		t.Fatalf("below-threshold nudge = %q, want none", got)
	}
	want := freshSessionNudgeText
	if got := freshSessionNudge(freshNudgeRecords(freshSessionNudgeTokens), statePath, session); got != want {
		t.Fatalf("threshold nudge = %q, want exact line %q", got, want)
	}
	if got := freshSessionNudge(freshNudgeRecords(freshSessionNudgeTokens+1), statePath, session); got != "" {
		t.Fatalf("repeat nudge in same stretch = %q, want none", got)
	}
	if got := freshSessionNudge(freshNudgeRecords(freshSessionNudgeTokens-1), statePath, session); got != "" {
		t.Fatalf("below-threshold reset nudge = %q, want none", got)
	}
	if got := freshSessionNudge(freshNudgeRecords(freshSessionNudgeTokens), statePath, session); got != want {
		t.Fatalf("nudge after reset = %q, want exact line %q", got, want)
	}
}

func TestFreshSessionNudgeUnknownContextDoesNotClearFlag(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	statePath := filepath.Join(t.TempDir(), "telemetry", "compaction_advisor.json")
	const session = "fresh-session-unknown"
	if got := freshSessionNudge(freshNudgeRecords(freshSessionNudgeTokens), statePath, session); got == "" {
		t.Fatal("expected initial nudge")
	}
	missingTranscript := filepath.Join(t.TempDir(), "missing-transcript.jsonl")
	if got := freshSessionNudge(readTranscriptRecords(missingTranscript), statePath, session); got != "" {
		t.Fatalf("unknown-context nudge = %q, want none", got)
	}
	if state := readAdvisorState(statePath, session); state["fresh_session_nudged"] != true {
		t.Fatalf("unknown context cleared nudge flag: %#v", state["fresh_session_nudged"])
	}
}

func TestFreshSessionNudgeRequiresDesktopEntrypoint(t *testing.T) {
	for _, entrypoint := range []string{"cli", ""} {
		t.Run(map[string]string{"cli": "cli", "": "empty"}[entrypoint], func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_ENTRYPOINT", entrypoint)
			statePath := filepath.Join(t.TempDir(), "telemetry", "compaction_advisor.json")
			if got := freshSessionNudge(freshNudgeRecords(freshSessionNudgeTokens), statePath, "other-entrypoint"); got != "" {
				t.Fatalf("entrypoint %q nudge = %q, want none", entrypoint, got)
			}
		})
	}
}

func TestFreshSessionNudgeWorksWhenCompactionAdvisorDisabledAndBelowItsThreshold(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PURSER_STATE_DIR", filepath.Join(t.TempDir(), "purser-state"))
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	if err := os.MkdirAll(filepath.Join(home, ".orchestrator"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".orchestrator", "config.toml"), []byte("[compaction]\nenabled = false\nthreshold_tokens = 250000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	command := testutil.Hide(exec.Command("git", "init", "--quiet", repo))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeAdvisorTranscript(t, transcript, freshNudgeRecords(freshSessionNudgeTokens))
	output := UserPromptSubmit(map[string]any{
		"session_id": "fresh-session-disabled-advisor", "cwd": repo, "transcript_path": transcript,
	}, time.Time{})
	if !strings.Contains(string(output), freshSessionNudgeText) {
		t.Fatalf("UserPromptSubmit output did not include nudge with advisor disabled and below its threshold: %s", output)
	}
}

func freshNudgeRecords(tokens int) []map[string]any {
	return []map[string]any{{
		"type":    "assistant",
		"message": map[string]any{"usage": map[string]any{"input_tokens": tokens}},
	}}
}
