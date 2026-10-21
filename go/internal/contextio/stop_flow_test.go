package contextio

import (
	"testing"
)

func TestPlanCodexDispatchedDetectsDispatcher(t *testing.T) {
	records := func(subagentType string) []map[string]any {
		return []map[string]any{
			{"type": "user", "message": map[string]any{"content": []any{"continue"}}},
			{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "name": "mcp__Agent", "input": map[string]any{"subagent_type": subagentType}},
			}}},
		}
	}

	tests := []struct {
		name         string
		subagentType string
		want         bool
	}{
		{name: "dispatcher without plan identity", subagentType: "purser:dispatcher", want: false},
		{name: "other subagent after latest user", subagentType: "general-purpose", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := planCodexDispatched(records(test.subagentType), 0, "plan-123"); got != test.want {
				t.Fatalf("planCodexDispatched() = %t, want %t", got, test.want)
			}
		})
	}
	identified := []map[string]any{
		{"type": "user", "message": map[string]any{"content": []any{"continue"}}},
		{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "name": "mcp__Agent", "input": map[string]any{"subagent_type": "purser:dispatcher", "prompt": "ORCH|task=plan-123-m1of2-p1c|phase=implement"}},
		}}},
	}
	if !planCodexDispatched(identified, 0, "plan-123") || planCodexDispatched(identified, 0, "plan-456") {
		t.Fatal("dispatcher suppression did not follow the identified plan")
	}
}

func latestGenuineUser(records []map[string]any) int {
	latest := -1
	for i, record := range records {
		if genuineTranscriptUser(record) {
			latest = i
		}
	}
	return latest
}
