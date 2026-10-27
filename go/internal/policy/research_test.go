package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResearchPolicy(t *testing.T) {
	configPath := filepath.Join("..", "..", "..", ".claude", "hooks", "orchestrator", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfig(config); err != nil {
		t.Fatalf("shipped config is invalid: %v", err)
	}

	for _, tc := range []struct {
		name        string
		effort      string
		unavailable bool
		model       string
		resolved    string
	}{
		{name: "default", model: "gpt-6.1-terra", resolved: "medium"},
		{name: "available primary at high", effort: "high", model: "gpt-6.1-terra", resolved: "high"},
		{name: "scoped fallback", effort: "medium", unavailable: true, model: "gpt-6-luna", resolved: "medium"},
		{name: "synthesis fallback", effort: "high", unavailable: true, model: "gpt-6.1-sol", resolved: "high"},
		{name: "synthesis preserves requested effort", effort: "xhigh", unavailable: true, model: "gpt-6.1-sol", resolved: "xhigh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveResearch(config, tc.effort, tc.unavailable)
			if !got.Valid || got.Model == nil || *got.Model != tc.model || got.Effort == nil || *got.Effort != tc.resolved {
				t.Fatalf("ResolveResearch = %#v, want %s/%s", got, tc.model, tc.resolved)
			}
		})
	}
	if got := ResolveResearch(config, "unsupported", false); got.Valid {
		t.Fatal("unsupported requested effort was accepted")
	}

	overridden := clonePolicyMap(config)
	research := overridden["routes"].(map[string]any)["research"].(map[string]any)
	research["model"], research["effort"] = "gpt-7-terra", "high"
	scoped := research["scoped_fallback"].(map[string]any)
	scoped["model"], scoped["effort"] = "gpt-7-luna", "low"
	synthesis := research["synthesis_fallback"].(map[string]any)
	synthesis["model"], synthesis["effort"], synthesis["minimum_effort"] = "gpt-7-sol", "xhigh", "xhigh"
	if err := ValidateConfig(overridden); err != nil {
		t.Fatalf("valid research overrides were rejected: %v", err)
	}
	for _, tc := range []struct {
		effort      string
		unavailable bool
		model       string
		resolved    string
	}{
		{model: "gpt-7-terra", resolved: "high"},
		{effort: "medium", model: "gpt-7-terra", resolved: "medium"},
		{effort: "high", unavailable: true, model: "gpt-7-luna", resolved: "low"},
		{effort: "xhigh", unavailable: true, model: "gpt-7-sol", resolved: "xhigh"},
	} {
		got := ResolveResearch(overridden, tc.effort, tc.unavailable)
		if !got.Valid || got.Model == nil || *got.Model != tc.model || got.Effort == nil || *got.Effort != tc.resolved {
			t.Fatalf("overridden ResolveResearch = %#v, want %s/%s", got, tc.model, tc.resolved)
		}
	}
	for _, pair := range []string{"primary", "scoped_fallback", "synthesis_fallback"} {
		for key, value := range map[string]string{"model": "provider/model", "effort": "unsupported"} {
			invalid := clonePolicyMap(config)
			entry := invalid["routes"].(map[string]any)["research"].(map[string]any)
			if pair != "primary" {
				entry = entry[pair].(map[string]any)
			}
			entry[key] = value
			if err := ValidateConfig(invalid); err == nil {
				t.Errorf("invalid research %s %s was accepted", pair, key)
			}
		}
	}
	synthesis["minimum_effort"] = "unsupported"
	if err := ValidateConfig(overridden); err == nil {
		t.Fatal("unsupported synthesis minimum_effort was accepted")
	}

	legacy := clonePolicyMap(config)
	delete(legacy["routes"].(map[string]any), "research")
	if err := ValidateConfig(legacy); err != nil {
		t.Fatalf("seven-route config was rejected: %v", err)
	}
	legacy["routes"].(map[string]any)["unexpected"] = map[string]any{"model": "gpt-6-luna", "effort": "medium"}
	if err := ValidateConfig(legacy); err == nil {
		t.Fatal("unexpected route was accepted")
	}

	malformed := clonePolicyMap(config)
	malformed["routes"].(map[string]any)["research"].(map[string]any)["synthesis_fallback"].(map[string]any)["effort"] = ""
	if err := ValidateConfig(malformed); err == nil {
		t.Fatal("malformed research fallback pair was accepted")
	}

	for _, tc := range []struct {
		phase string
		valid bool
	}{
		{phase: "operation", valid: true},
		{phase: "implementation"},
		{phase: "plan"},
	} {
		prompt := "ORCH|task=research|phase=" + tc.phase + "|route=research|scope=go"
		got := ParseOrchPrompt(prompt, config)
		if got.Valid != tc.valid {
			t.Errorf("ParseOrchPrompt phase=%s valid=%v, want %v (%s)", tc.phase, got.Valid, tc.valid, got.Code)
		}
	}

	root := t.TempDir()
	t.Setenv("PURSER_STATE_DIR", filepath.Join(root, "state"))
	userPath := filepath.Join(root, "config.toml")
	coderRoutes := []string{"mechanical", "subtle", "normal", "consequential", "escalated", "operation", "plan_standard"}
	for _, agent := range []string{"codex", "claude", "agy", "opencode"} {
		t.Run("overlay "+agent, func(t *testing.T) {
			var toml strings.Builder
			toml.WriteString("[roles]\ncoder = \"" + agent + "\"\n\n[coding]\ncoder_agent = \"" + agent + "\"\n")
			if agent == "agy" || agent == "opencode" {
				toml.WriteString("\n[coding.routes]\n")
				for _, route := range coderRoutes {
					if agent == "opencode" {
						toml.WriteString(route + " = \"openai/gpt-4o\"\n")
					} else {
						toml.WriteString(route + " = { model = \"gemini-2.5-pro\", effort = \"medium\" }\n")
					}
				}
			}
			if err := os.WriteFile(userPath, []byte(toml.String()), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadCoderConfig(configPath, userPath)
			if err != nil {
				t.Fatalf("LoadCoderConfig: %v", err)
			}
			if _, present := loaded["routes"].(map[string]any)["research"]; !present {
				t.Fatal("reserved research route was lost during coder overlay")
			}
			got := ResolveResearch(loaded, "medium", false)
			if agent == "codex" {
				if !got.Valid || got.Model == nil || *got.Model != "gpt-6.1-terra" || *got.Effort != "medium" {
					t.Fatalf("Codex research = %#v", got)
				}
			} else {
				operation := loaded["routes"].(map[string]any)["operation"].(map[string]any)
				if !got.Valid || got.Model == nil || *got.Model != operation["model"] || got.Effort == nil || *got.Effort != operation["effort"] {
					t.Fatalf("%s research = %#v, want operation %#v", agent, got, operation)
				}
			}
		})
	}
}

func clonePolicyMap(source map[string]any) map[string]any {
	data, _ := json.Marshal(source)
	var cloned map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	_ = decoder.Decode(&cloned)
	return cloned
}
