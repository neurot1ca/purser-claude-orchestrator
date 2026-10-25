package policy

import (
	"strings"
	"testing"
)

func TestParseOrchPromptAliases(t *testing.T) {
	for _, tc := range []struct{ key, value, want string }{
		{"phase", "implement", "implementation"}, {"phase", "IMPL", "implementation"},
		{"phase", "Code", "implementation"}, {"phase", "op", "operation"}, {"phase", "OPS", "operation"},
		{"reason", "feedback", "fix"}, {"reason", "review_feedback", "fix"}, {"reason", "REVIEW", "fix"},
		{"reason", "sendback", "fix"}, {"reason", "send-back", "fix"}, {"reason", "new", "new_task"}, {"reason", "CONT", "continue"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			phase, route := "implementation", "subtle"
			if tc.key == "phase" && (tc.value == "op" || strings.EqualFold(tc.value, "ops")) {
				phase, route = tc.value, "operation"
			}
			values := map[string]string{"task": "task-1", "phase": phase, "route": route, tc.key: tc.value}
			if phase == "implementation" {
				values["scope"] = "Src/KeepCase.go"
			}
			prompt := "ORCH"
			for _, key := range []string{"task", "phase", "route", "reason", "scope"} {
				if value, ok := values[key]; ok {
					prompt += "|" + key + "=" + value
				}
			}
			got := ParseOrchPrompt(prompt, policyTestConfig(t))
			if !got.Valid {
				t.Fatalf("alias parse failed: %#v", got)
			}
			actual := got.Phase
			if tc.key == "reason" {
				actual = got.Reason
			}
			if actual == nil || *actual != tc.want {
				t.Fatalf("%s normalized value = %v, want %q", tc.key, actual, tc.want)
			}
			if len(got.Normalizations) != 1 || got.Normalizations[0] != tc.key+"="+tc.value+"->"+tc.want {
				t.Fatalf("normalizations = %#v", got.Normalizations)
			}
			if tc.key == "phase" && tc.want == "implementation" && (len(got.Scope) != 1 || got.Scope[0] != "Src/KeepCase.go") {
				t.Fatalf("scope case changed during alias normalization: %#v", got.Scope)
			}
		})
	}
}

func TestParseOrchPromptUnknownEnumsRemainInvalid(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"phase", "build"}, {"reason", "review_comments"}, {"route", "normal-route"}} {
		values := map[string]string{"task": "task-1", "phase": "implementation", "route": "subtle", "scope": "Src/KeepCase.go"}
		values[tc.key] = tc.value
		prompt := "ORCH"
		for _, key := range []string{"task", "phase", "route", "reason", "scope"} {
			if value, ok := values[key]; ok {
				prompt += "|" + key + "=" + value
			}
		}
		got := ParseOrchPrompt(prompt, policyTestConfig(t))
		if got.Valid || got.Code != orchInvalidEnum || len(got.Normalizations) != 0 {
			t.Errorf("unknown %s=%s parse = %#v", tc.key, tc.value, got)
		}
	}
}
