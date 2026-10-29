package enforce

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"purser-hook/internal/testutil"
	"reflect"
	"runtime"
	"testing"
)

func TestPostAsyncClaimPythonFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".claude", "hooks", "orchestrator", "tests", "fixtures", "go_policy", "post_async_claim.json"))
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
	if fixture.Oracle != "codex._async_claim+_async_owned+_async_post_followup" || len(fixture.Cases) != 6 {
		t.Fatalf("unexpected async-claim fixture: oracle=%q cases=%d", fixture.Oracle, len(fixture.Cases))
	}
	for _, test := range fixture.Cases {
		t.Run(test["name"].(string), func(t *testing.T) {
			state := t.TempDir()
			repo := filepath.Join(state, "repo")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			if output, err := execGitInit(repo); err != nil {
				t.Fatalf("git init: %v: %s", err, output)
			}
			project := projectKey(&repo)
			input := replaceAsyncFixture(test["event"], repo, project).(map[string]any)
			index := replaceAsyncFixture(test["index"], repo, project).(map[string]any)
			writeAsyncFixtureObject(t, filepath.Join(state, "async_jobs"), inputJobID(input), index)
			if test["pending"] != nil {
				writeAsyncFixtureObject(t, filepath.Join(state, "pending"), stringValue(index["call_key"]), replaceAsyncFixture(test["pending"], repo, project))
			}
			if test["job"] != nil {
				writeAsyncFixtureObject(t, filepath.Join(state, "codex_exec_mcp", "jobs"), inputJobID(input), replaceAsyncFixture(test["job"], repo, project))
			}
			want := mapValue(test["expected"])
			switch test["name"] {
			case "claim_lock":
				path := filepath.Join(state, "async_jobs", inputJobID(input)+".claim.lock")
				lock, gotIndex, _, _, cached, err := claimAsyncResult(state, input, repo)
				if err != nil {
					t.Fatal(err)
				}
				wantSize := int64(0)
				if runtime.GOOS == "windows" {
					wantSize = 1
				}
				lockInfo, err := os.Stat(path)
				if err != nil || lockInfo.Size() != wantSize {
					t.Fatalf("fresh claim lock size = %v, err=%v; want %d", lockInfo, err, wantSize)
				}
				if runtime.GOOS == "windows" {
					marker := []byte{0}
					if n, readErr := lock.ReadAt(marker, 0); readErr != nil || n != 1 || marker[0] != '0' {
						t.Fatalf("fresh Windows claim lock marker = %q, n=%d, err=%v; want %q", marker, n, readErr, "0")
					}
				}
				if cached != nil || gotIndex["stage"] != want["stage"] {
					t.Fatalf("claim result stage=%v cached=%#v, want %v", gotIndex["stage"], cached, want["stage"])
				}
				response := map[string]any{"review": "cached"}
				indexPath := filepath.Join(state, "async_jobs", inputJobID(input)+".json")
				if err := consumeAsyncResult(indexPath, gotIndex, "bundle.json", response); err != nil {
					t.Fatal(err)
				}
				releaseTaskUsageLock(lock)
				lock, _, _, _, cached, err = claimAsyncResult(state, input, repo)
				if err != nil {
					t.Fatal(err)
				}
				defer releaseTaskUsageLock(lock)
				if !reflect.DeepEqual(cached, response) {
					t.Fatalf("second claim cached response = %#v, want %#v", cached, response)
				}
			case "consumed_cached_response":
				index["response"] = replaceAsyncFixture(mapValue(want)["cached"], repo, project)
				writeAsyncFixtureObject(t, filepath.Join(state, "async_jobs"), inputJobID(input), index)
				lock, _, _, _, cached, err := claimAsyncResult(state, input, repo)
				if err != nil {
					t.Fatal(err)
				}
				defer releaseTaskUsageLock(lock)
				if !reflect.DeepEqual(cached, replaceAsyncFixture(want["response"], repo, project)) {
					t.Fatalf("cached response = %#v, want %#v", cached, want["response"])
				}
			case "adoption_required", "malformed_index", "revalidation_changed_job":
				_, _, _, _, _, err := claimAsyncResult(state, input, repo)
				wantKind, _ := want["error"].(string)
				ownership, ok := err.(asyncOwnershipError)
				if !ok || ownership.kind != wantKind {
					t.Fatalf("claim error = %#v, want %s", err, wantKind)
				}
			case "adoption_allowed":
				lock, gotIndex, _, _, cached, err := claimAsyncResult(state, input, repo)
				if err != nil {
					t.Fatal(err)
				}
				defer releaseTaskUsageLock(lock)
				if cached != nil || gotIndex["stage"] != "FINALIZING" {
					t.Fatalf("adopted claim stage=%v cached=%#v", gotIndex["stage"], cached)
				}
			}
		})
	}
}

func TestConcurrentAsyncResultsForDifferentJobsBothConsume(t *testing.T) {
	state := t.TempDir()
	repo := filepath.Join(state, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := execGitInit(repo); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	project := projectKey(&repo)
	type result struct {
		bundle map[string]any
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, name := range []string{"one", "two"} {
		call := "codex async fixture " + name
		jobID := sha256Hex(call)
		event := map[string]any{"tool_name": "mcp__plugin_purser_codex-main__codex_result", "session_id": "session-" + name,
			"tool_use_id": "result-" + name, "tool_input": map[string]any{"jobId": jobID}}
		origin := map[string]any{"tool_name": "mcp__codex-main__codex", "session_id": "session-" + name, "tool_use_id": "origin-" + name}
		key, err := callKey(origin)
		if err != nil {
			t.Fatal(err)
		}
		index := map[string]any{"call": call, "call_key": key, "jobId": jobID, "project_key": project, "session_id": "session-" + name, "stage": "FINISHED"}
		pending := map[string]any{"async_call": call, "baseline": map[string]any{}, "job_id": jobID, "project_key": project,
			"session_id": "session-" + name, "task_id": "task-" + name, "tool_use_id": "origin-" + name}
		writeAsyncFixtureObject(t, filepath.Join(state, "async_jobs"), jobID, index)
		writeAsyncFixtureObject(t, filepath.Join(state, "pending"), key, pending)
		go func(event map[string]any, jobID string) {
			<-start
			lock, claimed, _, _, cached, claimErr := claimAsyncResult(state, event, repo)
			if claimErr == nil {
				bundle := map[string]any{"job": jobID, "result": "review bundle"}
				claimErr = consumeAsyncResult(filepath.Join(state, "async_jobs", jobID+".json"), claimed, "bundle.json", bundle)
				releaseTaskUsageLock(lock)
				_ = lock.Close()
				if cached != nil {
					claimErr = errors.New("unexpected cached response")
				}
				if claimErr == nil {
					results <- result{bundle: bundle}
					return
				}
			}
			results <- result{err: claimErr}
		}(event, jobID)
	}
	close(start)
	for range 2 {
		got := <-results
		if got.err != nil || got.bundle["result"] != "review bundle" {
			t.Fatalf("result consumption failed: %#v", got)
		}
	}
}

func TestAsyncResultDeliveryTelemetryFirstAndReplay(t *testing.T) {
	state := t.TempDir()
	repo := filepath.Join(state, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := execGitInit(repo); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	project := projectKey(&repo)
	jobID := "2235832b783efd26dc1e90c40f03a5b3604561d5815c955d2cdb72f87c3c6a9e"
	call := "codex async fixture"
	event := map[string]any{"tool_name": "mcp__codex-main__codex_result", "session_id": "session-delivery",
		"tool_input": map[string]any{"jobId": jobID}}
	origin := map[string]any{"tool_name": "mcp__codex-main__codex", "session_id": "session-delivery", "tool_use_id": "origin-delivery"}
	key, err := callKey(origin)
	if err != nil {
		t.Fatal(err)
	}
	index := map[string]any{"call": call, "call_key": key, "jobId": jobID, "project_key": project,
		"session_id": "session-delivery", "stage": "FINISHED"}
	pending := map[string]any{"async_call": call, "baseline": map[string]any{}, "job_id": jobID,
		"project_key": project, "session_id": "session-delivery", "task_id": "delivery-task", "tool_use_id": "origin-delivery",
		"repo_root": repo}
	writeAsyncFixtureObject(t, filepath.Join(state, "async_jobs"), jobID, index)
	writeAsyncFixtureObject(t, filepath.Join(state, "pending"), key, pending)

	lock, claimed, gotPending, _, cached, err := claimAsyncResult(state, event, repo)
	if err != nil {
		t.Fatal(err)
	}
	if cached != nil || claimed["result_delivery_sequence"] != float64(1) || claimed["result_delivery_outcome"] != "FINALIZED" {
		t.Fatalf("first delivery metadata = (%v, %v), cached=%#v", claimed["result_delivery_sequence"], claimed["result_delivery_outcome"], cached)
	}
	recordAsyncResultDelivery(state, event, gotPending, claimed)
	response := map[string]any{"review": "cached"}
	indexPath := filepath.Join(state, "async_jobs", jobID+".json")
	if err := consumeAsyncResult(indexPath, claimed, "bundle.json", response); err != nil {
		t.Fatal(err)
	}
	releaseTaskUsageLock(lock)
	_ = lock.Close()

	lock, replayed, gotPending, _, cached, err := claimAsyncResult(state, event, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { releaseTaskUsageLock(lock); _ = lock.Close() }()
	if !reflect.DeepEqual(cached, response) || replayed["result_delivery_sequence"] != float64(2) || replayed["result_delivery_outcome"] != "REPLAYED_CONSUMED" {
		t.Fatalf("replay metadata = (%v, %v), cached=%#v", replayed["result_delivery_sequence"], replayed["result_delivery_outcome"], cached)
	}
	persisted, err := readAsyncObject(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted["result_delivery_sequence"] != float64(2) || persisted["result_delivery_outcome"] != "REPLAYED_CONSUMED" {
		t.Fatalf("persisted replay metadata = (%v, %v)", persisted["result_delivery_sequence"], persisted["result_delivery_outcome"])
	}
	recordAsyncResultDelivery(state, event, gotPending, replayed)
	data, err := os.ReadFile(filepath.Join(state, "telemetry", "events", "by_task", pending["task_id"].(string)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("delivery telemetry events = %d, want 2; data=%s", len(events), data)
	}
	wantOutcomes := []string{"FINALIZED", "REPLAYED_CONSUMED"}
	for i, event := range events {
		payload := mapValue(event["payload"])
		if event["event_type"] != "ASYNC_RESULT_DELIVERED" || payload["job_id"] != jobID || payload["result_delivery_sequence"] != float64(i+1) || payload["result_delivery_outcome"] != wantOutcomes[i] {
			t.Fatalf("delivery event %d = %#v", i+1, event)
		}
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func execGitInit(repo string) ([]byte, error) {
	return testutil.Hide(exec.Command("git", "init", "-q", repo)).CombinedOutput()
}
