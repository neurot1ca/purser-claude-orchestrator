package contextio

import (
	"strings"
	"testing"
)

func TestFreshSessionNudgeWorkClassModels(t *testing.T) {
	for _, want := range []string{
		"runbook, release, CI rerun, measurement and known-cause fix sessions use model claude-sonnet-5-5 with effort medium",
		"unknown-cause bug investigation and design sessions use model claude-opus-5-5 with effort medium",
		"when the class is unclear, omit both so the session inherits this one",
	} {
		if !strings.Contains(freshSessionNudgeText, want) {
			t.Errorf("freshSessionNudgeText does not contain %q", want)
		}
	}
}

func TestPlanningHint(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{name: "plain planning word", prompt: "Please plan the migration", want: true},
		{name: "Turkish planning word", prompt: "Bunu planlayalım", want: true},
		{name: "Turkish phrase without planning word", prompt: "işleri başlatmakta sorun mu yaşıyor", want: false},
		{name: "planning word only in system reminder", prompt: "Question\n<system-reminder>Investigate unreliable /purser:plan offer</system-reminder>", want: false},
		{name: "unclosed system reminder drops rest", prompt: "Question\n<system-reminder>Investigate unreliable /purser:plan offer", want: false},
		{name: "command mid-sentence", prompt: "Please run /purser:plan when ready", want: false},
		{name: "command at start", prompt: "/purser:plan write a proposal", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planningHint(tt.prompt); got != tt.want {
				t.Fatalf("planningHint(%q) = %t, want %t", tt.prompt, got, tt.want)
			}
		})
	}
}
