package enforce

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeniedStartFailureHasNoAmbiguousContext(t *testing.T) {
	root := t.TempDir()
	event := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "mcp__codex-main__codex_start",
		"session_id": "denied-start-session", "tool_use_id": "denied-start-call", "tool_input": "malformed"}
	response, err := Pre(event, root)
	if err != nil {
		t.Fatal(err)
	}
	if mapValue(response["hookSpecificOutput"])["permissionDecision"] != "deny" {
		t.Fatalf("PreToolUse did not deny malformed start: %#v", response)
	}
	event["hook_event_name"] = "PostToolUseFailure"
	event["error"] = "blocked by PreToolUse"
	response = PostToolUseFailure(event, root)
	if response != nil {
		context := stringValue(mapValue(response["hookSpecificOutput"])["additionalContext"])
		if strings.Contains(context, "ambiguous outcome") || strings.Contains(context, "Evidence cleanup error") {
			t.Fatalf("denied start became ambiguous: %q", context)
		}
	}
	key, err := callKey(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "pending", key+".denied.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied marker was not consumed: %v", err)
	}
}

func TestFailureMissingPendingRemainsAmbiguous(t *testing.T) {
	event := map[string]any{"hook_event_name": "PostToolUseFailure", "tool_name": "mcp__codex-main__codex_start",
		"session_id": "missing-pending-session", "tool_use_id": "missing-pending-call"}
	response := PostToolUseFailure(event, t.TempDir())
	context := stringValue(mapValue(response["hookSpecificOutput"])["additionalContext"])
	if !strings.Contains(context, "ambiguous outcome") || !strings.Contains(context, "Evidence cleanup error: WiringError: pending call record is missing") {
		t.Fatalf("missing pending behavior changed: %q", context)
	}
}

func TestPostToolUseFailureDispatchesEveryCodexToolKind(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		{"mcp__codex-main__codex", "PostToolUseFailure"},
		{"mcp__codex-main__codex-reply", "PostToolUseFailure"},
		{"mcp__codex-main__codex_start", "PostToolUseFailure"},
		{"mcp__codex-main__codex_result", "PostToolUse"},
		{"mcp__codex-main__codex_cancel", "PostToolUse"},
	}
	for _, item := range cases {
		t.Run(strings.TrimPrefix(item.tool, "mcp__codex-main__"), func(t *testing.T) {
			response := PostToolUseFailure(map[string]any{"hook_event_name": "PostToolUseFailure", "tool_name": item.tool,
				"session_id": "dispatch-session", "tool_use_id": "dispatch-tool"}, t.TempDir())
			if response == nil {
				t.Fatal("dispatch returned no response")
			}
			output := mapValue(response["hookSpecificOutput"])
			if got := stringValue(output["hookEventName"]); got != item.want {
				t.Fatalf("hookEventName = %q, want %q; response=%#v", got, item.want, response)
			}
		})
	}
}
