package policy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonOrchGoldenParity(t *testing.T) {
	path := filepath.Join("..", "..", "..", ".claude", "hooks", "orchestrator", "tests", "fixtures", "go_policy", "orch.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string `json:"name"`
		Input struct {
			Prompt any            `json:"prompt"`
			Config map[string]any `json:"config"`
		} `json:"input"`
		ExpectedCode    string         `json:"expected_code"`
		ExpectedMessage string         `json:"expected_message"`
		OK              bool           `json:"ok"`
		Result          map[string]any `json:"result"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := ParseOrchPrompt(tc.Input.Prompt, tc.Input.Config)
			gotJSON, _ := json.Marshal(got)
			var gotFields map[string]any
			if err := json.Unmarshal(gotJSON, &gotFields); err != nil {
				t.Fatal(err)
			}
			if got.Valid != tc.OK || !jsonEqual(gotFields, tc.Result) {
				wantJSON, _ := json.Marshal(tc.Result)
				t.Fatalf("ParseOrchPrompt = %s, want %s", gotJSON, wantJSON)
			}
			if !tc.OK && (got.Code != tc.ExpectedCode || got.Message != tc.ExpectedMessage) {
				t.Fatalf("rejected on (%q, %q), want branch (%q, %q)", got.Code, got.Message, tc.ExpectedCode, tc.ExpectedMessage)
			}
		})
	}
}

func TestOrchEnumAliasesAndCorrectedLineValidity(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".claude", "hooks", "orchestrator", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		t.Fatal(err)
	}

	aliasPrompts := []struct {
		prompt string
		phase  string
		route  string
		reason string
	}{
		{"ORCH|task=t1|phase=implementation|route=subtle|reason=review_fix|scope=go/internal/policy\nbody", "implementation", "subtle", "fix"},
		{"ORCH|task=t1|phase=implementation|route=subtle|reason=review-fix|scope=go/internal/policy\nbody", "implementation", "subtle", "fix"},
		{"ORCH|task=t1|phase=implementation|route=subtle|reason=sendback|scope=go/internal/policy\nbody", "implementation", "subtle", "fix"},
		{"ORCH|task=t1|phase=implementation|route=subtle|reason=send_back|scope=go/internal/policy\nbody", "implementation", "subtle", "fix"},
		{"ORCH|task=t1|phase=implementation|route=subtle|reason=send-back|scope=go/internal/policy\nbody", "implementation", "subtle", "fix"},
		{"ORCH|task=t1|phase=implementation|route=subtle|reason=repair|scope=go/internal/policy\nbody", "implementation", "subtle", "fix"},
		{"ORCH|task=t1|phase=coding|route=mech|reason=re-anchor|scope=go/internal/policy\nbody", "implementation", "mechanical", "reanchor"},
		{"ORCH|task=t1|phase=planning|route=plan|reason=override\nbody", "plan", "plan_standard", "user_override"},
	}
	for _, tc := range aliasPrompts {
		got := ParseOrchPrompt(tc.prompt, config)
		if !got.Valid || *got.Phase != tc.phase || *got.Route != tc.route || *got.Reason != tc.reason {
			t.Fatalf("ParseOrchPrompt(%q) = %+v, want valid %s/%s/%s", tc.prompt, got, tc.phase, tc.route, tc.reason)
		}
	}

	placeholders := []string{
		"phase=<implementation|operation|plan>",
		"reason=<new_task|continue|fix|route_change|reanchor|repeated_failure|user_override>",
		"route=<mechanical|subtle|normal|consequential|escalated|operation|plan_standard|research>",
	}
	hasPlaceholder := func(line string) bool {
		for _, p := range placeholders {
			if strings.Contains(line, p) {
				return true
			}
		}
		return false
	}

	invalidPrompts := []string{
		"ORCH|task=t1|phase=bogus|route=subtle\nbody",
		"ORCH|task=t1|phase=implementaton|route=subtle\nbody",
		"ORCH|task=t1|phase=implementation|route=subtle|reason=totally_unknown_reason|scope=go/internal/policy\nbody",
		"ORCH|task=t1|phase=implementation|route=subtle|reason=xyz|scope=go/internal/policy\nbody",
		"ORCH|task=t1|phase=implementation|route=subtle|reason=fixx|scope=go/internal/policy\nbody",
		"ORCH|task=t1|phase=implementation|route=unknown_route|scope=go/internal/policy\nbody",
		"ORCH|task=t1|phase=implementation|route=subtl|scope=go/internal/policy\nbody",
		"ORCH|task=t1|phase=implementation|route=subtle|requested=invalid_requester|scope=go/internal/policy\nbody",
		"ORCH|task=t1|phase=implementation|route=subtle\nbody",
		"ORCH|task=t1|phase=implementation|route=normal|scope=go/internal/policy\nbody",
		"ORCH|task=t1|phase=plan|route=unknown_route\nbody",
		"ORCH|task=t1|phase=bogus|route=plan_standard\nbody",
		"ORCH|task=t1|phase=bogus|route=research\nbody",
	}
	for _, prompt := range invalidPrompts {
		got := ParseOrchPrompt(prompt, config)
		if got.Valid || got.Correction == "" {
			t.Fatalf("ParseOrchPrompt(%q) = %+v, want rejection with Correction", prompt, got)
		}
		reparsed := ParseOrchPrompt(got.Correction+"\nbody", config)
		if !reparsed.Valid && !hasPlaceholder(got.Correction) {
			t.Fatalf("Correction %q for %q neither parses (code=%s msg=%q) nor contains an enum placeholder", got.Correction, prompt, reparsed.Code, reparsed.Message)
		}
	}
}
