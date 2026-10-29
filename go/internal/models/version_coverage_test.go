package models

import "testing"

func TestSameFamily(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"gpt-6-luna", "gpt-6.1-luna", true},
		{"claude-5-opus", "claude-5.1-opus", true},
		{"gpt-6-luna", "gpt-6-luna", true},
		{"gpt-6-luna", "gpt-6-codex", false},
		{"gpt-6-luna", "claude-6-luna", false},
		{"gpt-luna", "gpt-6-luna", false},
		{"malformed", "malformed", true},
		{"", "", true},
		{"", "gpt-6-luna", false},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			if got := SameFamily(tt.a, tt.b); got != tt.want {
				t.Fatalf("SameFamily(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestAtLeast(t *testing.T) {
	tests := []struct {
		floor, model string
		want         bool
	}{
		{"gpt-6-luna", "gpt-6-luna", true},
		{"gpt-6-luna", "gpt-6.1-luna", true},
		{"gpt-6.1-luna", "gpt-6-luna", false},
		{"gpt-6-luna", "gpt-6-codex", false},
		{"gpt-luna", "gpt-6-luna", false},
		{"gpt-6-luna", "malformed", false},
		{"malformed", "malformed", true},
		{"", "", true},
		{"", "gpt-6-luna", false},
	}
	for _, tt := range tests {
		t.Run(tt.floor+"_to_"+tt.model, func(t *testing.T) {
			if got := AtLeast(tt.floor, tt.model); got != tt.want {
				t.Fatalf("AtLeast(%q, %q) = %v, want %v", tt.floor, tt.model, got, tt.want)
			}
		})
	}
}

func TestNewestVersion(t *testing.T) {
	tests := []struct {
		ids  []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"gpt-6-luna"}, "gpt-6-luna"},
		{[]string{"gpt-6-luna", "gpt-6.1-luna"}, "gpt-6.1-luna"},
		{[]string{"gpt-6.1-luna", "gpt-6-luna"}, "gpt-6.1-luna"},
		{[]string{"gpt-6.1-luna", "gpt-6.1-luna"}, "gpt-6.1-luna"},
		{[]string{"gpt-6-luna", "gpt-6-codex"}, "gpt-6-luna"},
		{[]string{"bad", "also-bad"}, "bad"},
	}
	for i, tt := range tests {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			if got := NewestVersion(tt.ids); got != tt.want {
				t.Fatalf("NewestVersion(%v) = %q, want %q", tt.ids, got, tt.want)
			}
		})
	}
}
