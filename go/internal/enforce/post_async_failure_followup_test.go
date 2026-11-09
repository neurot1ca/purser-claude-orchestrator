package enforce

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"purser-hook/internal/contextio"
	"purser-hook/internal/lease"
)

func TestPostAsyncFailureFollowupPythonFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", ".claude", "hooks", "orchestrator", "tests", "fixtures", "go_policy", "failure_process.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Oracle string           `json:"oracle"`
		Cases  []map[string]any `json:"cases"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Oracle != "Python PostToolUseFailure real hook process behavior" {
		t.Fatalf("unexpected follow-up fixture: oracle=%q cases=%d", fixture.Oracle, len(fixture.Cases))
	}
	selected := 0
	for _, item := range fixture.Cases {
		if item["async_initial"] == nil {
			continue
		}
		selected++
		t.Run(stringValue(item["name"]), func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			initial := mapValue(replaceAsyncFixture(item["async_initial"], repo, "0123456789abcdef"))
			event, index := mapValue(initial["event"]), mapValue(initial["index"])
			pending := mapValue(initial["pending"])
			id, key := stringValue(index["jobId"]), stringValue(index["call_key"])
			writeAsyncFixtureObject(t, filepath.Join(root, "async_jobs"), id, index)
			writeAsyncFixtureObject(t, filepath.Join(root, "pending"), key, pending)
			if initial["job"] != nil {
				writeAsyncFixtureObject(t, filepath.Join(root, "codex_exec_mcp", "jobs"), id, initial["job"])
			}
			acquired := lease.New(filepath.Join(root, "projects", stringValue(pending["project_key"]))).Acquire(
				stringValue(pending["lease_owner"]), stringValue(pending["task_id"]), []string{"src"}, lease.AcquireOptions{}, 100)
			if acquired.Status != "OK" {
				t.Fatalf("seed lease: %#v", acquired)
			}
			if item["evidence"] != nil {
				seed := restoreFailureState(item["evidence"], root, stringValue(pending["project_key"]))
				if _, err := persistFailureEvidence(root, key, seed.(map[string]any)); err != nil {
					t.Fatal(err)
				}
			}
			if item["name"] == "claim_lock_contention" {
				lockPath := filepath.Join(root, "async_jobs", id+".claim.lock")
				blocker, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err = contextio.TryLock(blocker); err != nil {
					t.Fatal(err)
				}
				got := PostToolUseFailureAsyncFollowup(event, root)
				releaseTaskUsageLock(blocker)
				if !reflect.DeepEqual(replaceFailureState(got, root), item["response"]) {
					t.Fatalf("response = %#v, want %#v", replaceFailureState(got, root), item["response"])
				}
				return
			}
			got := PostToolUseFailureAsyncFollowup(event, root)
			if !reflect.DeepEqual(replaceFailureState(got, root), item["response"]) {
				t.Fatalf("response = %#v, want %#v", replaceFailureState(got, root), item["response"])
			}
			stored, err := readAsyncObject(filepath.Join(root, "async_jobs", id+".json"))
			if err != nil || stored["stage"] != "CONSUMED" || !reflect.DeepEqual(stored["response"], got) {
				t.Fatalf("consumed index = %#v, %v", stored, err)
			}
		})
	}
	if selected != 6 {
		t.Fatalf("follow-up fixture cases = %d, want 6", selected)
	}
}

func TestPostAsyncFailureFollowupDoesNotConsumeStillRunningResult(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".claude", "hooks", "orchestrator", "tests", "fixtures", "go_policy", "post_async_failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []map[string]any `json:"cases"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil || len(fixture.Cases) == 0 {
		t.Fatalf("decode async failure fixture: cases=%d err=%v", len(fixture.Cases), err)
	}
	for _, state := range []string{"RUNNING", "SUCCEEDED"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			item := fixture.Cases[0]
			event, index := mapValue(item["event"]), mapValue(item["index"])
			pending, job := mapValue(item["pending"]), mapValue(item["job"])
			index["project_key"], pending["project_key"], index["stage"] = "0123456789abcdef", "0123456789abcdef", "RUNNING"
			job["state"] = state
			event["error"] = `{"threadId": null, "error": "ASYNC_STILL_RUNNING: still running; wait for the completion signal"}`
			key, id := stringValue(index["call_key"]), stringValue(index["jobId"])
			writeAsyncFixtureObject(t, filepath.Join(root, "async_jobs"), id, index)
			writeAsyncFixtureObject(t, filepath.Join(root, "pending"), key, pending)
			writeAsyncFixtureObject(t, filepath.Join(root, "codex_exec_mcp", "jobs"), id, job)
			if got := lease.New(filepath.Join(root, "projects", stringValue(pending["project_key"]))).Acquire(
				stringValue(pending["lease_owner"]), stringValue(pending["task_id"]), []string{"src"}, lease.AcquireOptions{}, 100); got.Status != "OK" {
				t.Fatalf("seed lease: %#v", got)
			}
			indexPath := filepath.Join(root, "async_jobs", id+".json")
			pendingPath := filepath.Join(root, "pending", key+".json")
			leasePath := filepath.Join(root, "projects", stringValue(pending["project_key"]), "scope_leases.json")
			beforeIndex, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			beforePending, err := os.ReadFile(pendingPath)
			if err != nil {
				t.Fatal(err)
			}
			beforeLease, err := os.ReadFile(leasePath)
			if err != nil {
				t.Fatal(err)
			}
			if got := PostToolUseFailureAsyncFollowup(event, root); got != nil {
				t.Fatalf("failure response = %#v, want nil", got)
			}
			for _, check := range []struct {
				path   string
				before []byte
			}{{indexPath, beforeIndex}, {pendingPath, beforePending}, {leasePath, beforeLease}} {
				after, readErr := os.ReadFile(check.path)
				if readErr != nil || !reflect.DeepEqual(after, check.before) {
					t.Fatalf("state changed at %s: read err=%v", check.path, readErr)
				}
			}
			for _, path := range []string{filepath.Join(root, "async_jobs", id+".claim.lock"), filepath.Join(root, "raw_tool_outputs", key+"-failure.json")} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("unexpected state write %s: %v", path, statErr)
				}
			}
		})
	}
}

func TestPostAsyncFailureFollowupTerminalFailedResultFinalizes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".claude", "hooks", "orchestrator", "tests", "fixtures", "go_policy", "post_async_failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []map[string]any `json:"cases"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil || len(fixture.Cases) == 0 {
		t.Fatalf("decode async failure fixture: cases=%d err=%v", len(fixture.Cases), err)
	}
	root := t.TempDir()
	item := fixture.Cases[0]
	event, index := mapValue(item["event"]), mapValue(item["index"])
	pending, job := mapValue(item["pending"]), mapValue(item["job"])
	index["project_key"], pending["project_key"], index["stage"] = "0123456789abcdef", "0123456789abcdef", "FINALIZING"
	job["state"] = "FAILED"
	event["error"] = "Codex execution failed"
	key, id := stringValue(index["call_key"]), stringValue(index["jobId"])
	writeAsyncFixtureObject(t, filepath.Join(root, "async_jobs"), id, index)
	writeAsyncFixtureObject(t, filepath.Join(root, "pending"), key, pending)
	writeAsyncFixtureObject(t, filepath.Join(root, "codex_exec_mcp", "jobs"), id, job)
	if got := lease.New(filepath.Join(root, "projects", stringValue(pending["project_key"]))).Acquire(
		stringValue(pending["lease_owner"]), stringValue(pending["task_id"]), []string{"src"}, lease.AcquireOptions{}, 100); got.Status != "OK" {
		t.Fatalf("seed lease: %#v", got)
	}
	if response := PostToolUseFailureAsyncFollowup(event, root); response == nil {
		t.Fatal("terminal failure returned nil")
	}
	stored, err := readAsyncObject(filepath.Join(root, "async_jobs", id+".json"))
	if err != nil || stored["stage"] != "CONSUMED" || stored["terminal_failure"] != true {
		t.Fatalf("terminal failure index = %#v, err=%v", stored, err)
	}
}

func replaceFailureState(value any, root string) any {
	switch item := value.(type) {
	case string:
		escapedRoot := strings.ReplaceAll(root, `\`, `\\`)
		item = strings.ReplaceAll(item, escapedRoot, "__STATE__")
		item = strings.ReplaceAll(item, root, "__STATE__")
		if strings.Contains(item, "__STATE__") {
			item = strings.ReplaceAll(item, `\\`, "/")
			item = strings.ReplaceAll(item, `\`, "/")
		}
		return item
	case []any:
		out := make([]any, len(item))
		for i, child := range item {
			out[i] = replaceFailureState(child, root)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, child := range item {
			if key == "project_key" && child != nil {
				out[key] = "__PROJECT_KEY__"
			} else {
				out[key] = replaceFailureState(child, root)
			}
		}
		return out
	default:
		return value
	}
}

func restoreFailureState(value any, root, project string) any {
	switch item := value.(type) {
	case string:
		return strings.ReplaceAll(strings.ReplaceAll(item, "__STATE__", root), "__PROJECT_KEY__", project)
	case []any:
		out := make([]any, len(item))
		for i, child := range item {
			out[i] = restoreFailureState(child, root, project)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, child := range item {
			if key == "project_key" && child == "__PROJECT_KEY__" {
				out[key] = project
			} else {
				out[key] = restoreFailureState(child, root, project)
			}
		}
		return out
	default:
		return value
	}
}
