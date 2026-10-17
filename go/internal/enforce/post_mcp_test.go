package enforce

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"purser-hook/internal/waiter"
)

func TestMCPPostRecoveryAndContext(t *testing.T) {
	root, owner, event := mcpFixture(t, "post-recovery", "codex")
	t.Setenv("PURSER_SESSION_OWNER", owner.String())
	original := runPreDecision
	defer func() { runPreDecision = original }()
	preparations := 0
	runPreDecision = func(map[string]any, string, map[string]any) (map[string]any, error) {
		preparations++
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "updatedInput": map[string]any{"prompt": "effective", "cwd": root}, "additionalContext": "exceptional guidance"}}, nil
	}
	if err := PublishMCPHandoff(root, owner, event); err != nil {
		t.Fatal(err)
	}
	h, err := PrepareMCP(root, owner, "post-recovery", stringValue(event["tool_name"]), mapValue(event["tool_input"]))
	if err != nil {
		t.Fatal(err)
	}
	event["hook_event_name"] = "PostToolUse"
	event["cwd"], event["agent_id"] = "untrusted cwd", "untrusted caller"
	restored, context, err := restoreMCPPostEvent(event, root)
	if err != nil || !reflect.DeepEqual(restored["tool_input"], h.Effective) || restored["cwd"] != root || restored["agent_id"] != nil || context != "exceptional guidance" || preparations != 1 {
		t.Fatalf("recovery: %v %v %q preparations=%d", restored, err, context, preparations)
	}
	if mapValue(event["tool_input"])["prompt"] != "same" {
		t.Fatal("post event mutated")
	}
	for _, name := range []string{"PostToolUse", "PostToolUseFailure"} {
		event["hook_event_name"] = name
		var response map[string]any
		if name == "PostToolUse" {
			response, _ = PostToolUse(event, root)
		} else {
			response = PostToolUseFailure(event, root)
		}
		if got := stringValue(mapValue(response["hookSpecificOutput"])["additionalContext"]); !strings.Contains(got, context) {
			t.Fatalf("%s lost context: %v", name, response)
		}
	}
	if preparations != 1 {
		t.Fatal("post invoked preparation again")
	}
	event["tool_input"] = map[string]any{"prompt": "changed"}
	if _, _, err := restoreMCPPostEvent(event, root); err == nil {
		t.Fatal("changed post input accepted")
	}
	event["tool_input"] = h.Input
	event["session_id"] = "other-session"
	if _, _, err := restoreMCPPostEvent(event, root); err == nil {
		t.Fatal("other session accepted")
	}
}

func TestMCPFailureMatchingHandoff(t *testing.T) {
	for _, artifact := range []bool{false, true} {
		t.Run(map[bool]string{false: "result", true: "artifact"}[artifact], func(t *testing.T) {
			plugin, err := filepath.Abs(filepath.Join("..", "..", ".."))
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAUDE_PLUGIN_ROOT", plugin)
			t.Setenv("USERPROFILE", t.TempDir())
			root, repo, event, index, pending, start, _, _, _ := newAsyncSuccessCase(t, "mcp_failure")
			owner, err := waiter.PinOwner(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("PURSER_SESSION_OWNER", owner.String())
			prior := runPreDecision
			t.Cleanup(func() { runPreDecision = prior })
			runPreDecision = func(map[string]any, string, map[string]any) (map[string]any, error) {
				return map[string]any{}, nil
			}
			prepare := func(e map[string]any) {
				t.Helper()
				e["hook_event_name"] = "PreToolUse"
				if err := PublishMCPHandoff(root, owner, e); err != nil {
					t.Fatal(err)
				}
				if h, err := PrepareMCP(root, owner, stringValue(e["tool_use_id"]), stringValue(e["tool_name"]), mapValue(e["tool_input"])); err != nil || h == nil || mapValue(h.Response["hookSpecificOutput"])["permissionDecision"] == "deny" {
					t.Fatalf("prepare: %#v, %v", h, err)
				}
			}
			start["tool_name"], start["cwd"], start["tool_input"] = "mcp__plugin_purser_codex-main__codex_start", repo, map[string]any{"prompt": "Plan the change"}
			prepare(start)
			index["stage"], pending["phase"] = "FINISHED", "plan"
			indexPath := filepath.Join(root, "async_jobs", stringValue(index["jobId"])+".json")
			if err := writeAsyncObject(indexPath, index); err != nil {
				t.Fatal(err)
			}
			pendingPath := pendingPathForAsync(root, start)
			if err := writeAsyncObject(pendingPath, pending); err != nil {
				t.Fatal(err)
			}
			event["tool_name"], event["cwd"] = "mcp__plugin_purser_codex-main__codex_result", repo
			if artifact {
				mapValue(event["tool_input"])["artifact"] = "plan"
			}
			prepare(event)
			event["hook_event_name"], event["error"] = "PostToolUseFailure", "Tool request failed"
			response := PostToolUseFailure(event, root)
			if message := stringValue(mapValue(response["hookSpecificOutput"])["additionalContext"]); strings.Contains(message, "Evidence cleanup error") {
				t.Fatalf("matching handoff failed: %s", message)
			}
			if artifact {
				stored, err := readAsyncObject(indexPath)
				if err != nil || !reflect.DeepEqual(stored, index) {
					t.Fatalf("artifact failure changed the index: %#v, %v", stored, err)
				}
				if _, err := os.Stat(pendingPath); err != nil {
					t.Fatalf("artifact failure lost pending work: %v", err)
				}
			}
			event["tool_use_id"], event["tool_input"] = "finalize-after-failure", map[string]any{"jobId": index["jobId"]}
			prepare(event)
			event["hook_event_name"] = "PostToolUse"
			response, err = PostToolUse(event, root)
			if err != nil || mapValue(response["hookSpecificOutput"])["hookEventName"] != "PostToolUse" {
				t.Fatalf("following finalize returned invalid helper output: %#v, %v", response, err)
			}
			if artifact {
				if path := stringValue(responseBundle(t, response)["plan_path"]); path == "" {
					t.Fatal("following finalize did not publish the plan")
				}
				event["tool_use_id"], event["tool_input"] = "artifact-after-finalize", map[string]any{"jobId": index["jobId"], "artifact": "plan"}
				prepare(event)
				event["hook_event_name"] = "PostToolUse"
				if response, err := PostToolUse(event, root); err != nil || len(response) != 0 {
					t.Fatalf("artifact after finalize: %#v, %v", response, err)
				}
			}
		})
	}
}

func TestMCPFailureRejectsMismatchedHandoff(t *testing.T) {
	root, owner, event := mcpFixture(t, "failure-mismatch", "codex_result")
	t.Setenv("PURSER_SESSION_OWNER", owner.String())
	tool, input := event["tool_name"], event["tool_input"]
	if err := PublishMCPHandoff(root, owner, event); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareMCP(root, owner, stringValue(event["tool_use_id"]), stringValue(tool), mapValue(input)); err != nil {
		t.Fatal(err)
	}
	event["hook_event_name"] = "PostToolUseFailure"
	for _, mismatch := range []struct {
		tool  any
		input any
	}{
		{tool, map[string]any{"jobId": "different"}},
		{"mcp__plugin_purser_codex-main__codex_cancel", input},
	} {
		event["tool_name"], event["tool_input"] = mismatch.tool, mismatch.input
		response := PostToolUseFailure(event, root)
		if !strings.Contains(stringValue(mapValue(response["hookSpecificOutput"])["additionalContext"]), "MCP post handoff binding mismatch") {
			t.Fatalf("mismatched failure accepted: %#v", response)
		}
	}
}

func TestMCPPostLegacyAndNormalOutputUnchanged(t *testing.T) {
	root, owner, event := mcpFixture(t, "legacy", "codex")
	t.Setenv("PURSER_SESSION_OWNER", owner.String())
	if restored, context, err := restoreMCPPostEvent(event, root); err != nil || context != "" || !reflect.DeepEqual(restored, event) {
		t.Fatalf("legacy recovery: %v", err)
	}
	response := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PostToolUse", "updatedToolOutput": []any{map[string]any{"type": "text", "text": "bundle"}}}}
	before := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PostToolUse", "updatedToolOutput": []any{map[string]any{"type": "text", "text": "bundle"}}}}
	if got := appendMCPPostContext(response, "PostToolUse", ""); !reflect.DeepEqual(got, before) {
		t.Fatal("normal output grew")
	}
}

func TestMCPPostBypassContext(t *testing.T) {
	root, owner, event := mcpFixture(t, "bypass-post", "codex")
	t.Setenv("PURSER_SESSION_OWNER", owner.String())
	original := runPreDecision
	defer func() { runPreDecision = original }()
	runPreDecision = func(map[string]any, string, map[string]any) (map[string]any, error) {
		return nil, errors.New("injected failure")
	}
	if err := PublishMCPHandoff(root, owner, event); err != nil {
		t.Fatal(err)
	}
	h, _ := PrepareMCP(root, owner, "bypass-post", stringValue(event["tool_name"]), mapValue(event["tool_input"]))
	restored, context, err := restoreMCPPostEvent(event, root)
	if h == nil || err != nil || context == "" || !reflect.DeepEqual(restored["tool_input"], event["tool_input"]) {
		t.Fatalf("bypass recovery: %v %s", err, context)
	}
}
