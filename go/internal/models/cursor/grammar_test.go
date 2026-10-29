package cursor

import "testing"

func TestValidModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"auto", true},
		{"gpt-5.3-codex", true},
		{"cursor/claude-opus_4.1", true},
		{"latest:claude-opus-high", true},
		{"", false},
		{"latest:", false},
		{"latest:auto", false},
		{"model with spaces", false},
		{"model:tag", false},
		{"-model", true}, // Listing IDs permit any nonempty sequence of allowed characters.
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := ValidModel(tt.model); got != tt.want {
				t.Fatalf("ValidModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestVendorFamily(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"gpt-5-codex", "openai"},
		{"codex-mini", "openai"},
		{"latest:claude-opus", "anthropic"},
		{"cursor-gemini-2", "google"},
		{"grok-4", "xai"},
		{"composer-2", "cursor"},
		{"kimi-k3", "moonshot"},
		{"glm-4", "zhipu"},
		{"auto", ""},
		{"unknown-1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := VendorFamily(tt.model); got != tt.want {
				t.Fatalf("VendorFamily(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestFamily(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"auto", ""},
		{"latest:claude-opus-5-5-high", "claude-opus-high"},
		{"cursor-gpt-5.3-codex-high", "gpt-codex-high"},
		{"kimi-k3-high", "kimi-high"},
		{"kimi-k3.1-high", "kimi-high"},
		{"kimi-k-high", "kimi-k-high"},
		{"composer-2.5", "composer"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := Family(tt.model); got != tt.want {
				t.Fatalf("Family(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestResolveLatest(t *testing.T) {
	ids := []string{"auto", "claude-opus-5-high", "claude-opus-5-5-high", "gpt-5.2-codex", "kimi-k3-high"}
	tests := []struct {
		family string
		want   string
	}{
		{"claude-opus-high", "claude-opus-5-5-high"},
		{"gpt-codex", "gpt-5.2-codex"},
		{"kimi-high", "kimi-k3-high"},
		{"missing", ""},
		{"", ""},
		{"auto", ""},
	}
	for _, tt := range tests {
		t.Run(tt.family, func(t *testing.T) {
			if got := ResolveLatest(ids, tt.family); got != tt.want {
				t.Fatalf("ResolveLatest(%v, %q) = %q, want %q", ids, tt.family, got, tt.want)
			}
		})
	}
	if got := ResolveLatest(nil, "claude-opus-high"); got != "" {
		t.Fatalf("ResolveLatest(nil, family) = %q, want empty", got)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"5.10", "5.2", 1},
		{"5.2", "5.10", -1},
		{"5.2", "5.2", 0},
		{"5.2", "5.2.0", -1},
		{"", "1", -1},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			if got := compareVersions(versionTokens(tt.a), versionTokens(tt.b)); got != tt.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
