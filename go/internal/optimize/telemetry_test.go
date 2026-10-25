package optimize

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"purser-hook/internal/contextio"
)

func telemetryWrite(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func telemetryID(letter string) string { return strings.Repeat(letter, 64) }

func telemetryFixture(t *testing.T) (string, string, string) {
	t.Helper()
	state, repo := t.TempDir(), t.TempDir()
	project := contextio.ProjectKey(repo)
	jobs := filepath.Join(state, "codex_exec_mcp", "jobs")
	telemetryWrite(t, filepath.Join(jobs, telemetryID("a")+".json"), map[string]any{
		"jobId": telemetryID("a"), "taskId": "task-1", "project_key": project, "state": "SUCCEEDED", "finishedAt": 100,
		"reviewer1Valid": 0,
		"reviewerOutcome": map[string]any{
			"execution_status": "COMPLETED", "verdict": "PASS", "provider": "raw-provider",
			"requested_model": "raw-model", "served_model_attestation": "attested-raw",
		},
	})
	telemetryWrite(t, filepath.Join(jobs, telemetryID("b")+".json"), map[string]any{
		"jobId": telemetryID("b"), "taskId": "task-1", "project_key": project, "state": "FAILED", "finishedAt": 90,
		"reviewerOutcome": map[string]any{"execution_status": "COMPLETED", "verdict": "FAIL", "provider": "p", "requested_model": "m"},
	})
	telemetryWrite(t, filepath.Join(jobs, telemetryID("c")+".json"), map[string]any{
		"jobId": telemetryID("c"), "taskId": "task-b", "project_key": "other-project", "state": "SUCCEEDED", "finishedAt": 120,
	})
	if err := os.WriteFile(filepath.Join(jobs, "broken.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	telemetryWrite(t, filepath.Join(state, "telemetry", "learning_summary.json"), map[string]any{
		"schema_version": 1,
		"sources":        []any{},
		"cohorts":        map[string]any{},
		"archive":        map[string]any{"covered_sources": 4, "evicted_records": 2},
		"records": []any{
			map[string]any{"id": telemetryID("a"), "task": "task-1", "project_key": project, "review_model": "archive-wrong", "reviewer1_valid": 9, "reviewer1_valid_present": true, "at": 200},
			map[string]any{"id": telemetryID("d"), "task": "task-2", "project_key": project, "review_execution_status": "COMPLETED", "review_verdict": "FAIL", "review_provider": "provider-d", "review_model": "model-d", "reviewer1_false": 0, "reviewer1_false_present": true, "send_backs": 2, "at": 150},
			map[string]any{"id": telemetryID("e"), "task": "task-b", "project_key": "other-project", "at": 160},
			map[string]any{"id": telemetryID("f"), "task": "task-5", "project_key": project, "at": 110},
		},
	})
	tasks := filepath.Join(state, "projects", project, "telemetry", "tasks")
	telemetryWrite(t, filepath.Join(tasks, "task-1.json"), map[string]any{
		"project_key": project, "task_id": "task-1",
		"claude_acceptance": map[string]any{"outcome": "accepted", "send_backs": 0, "applied_as_is": true},
	})
	telemetryWrite(t, filepath.Join(tasks, "task-2.json"), map[string]any{
		"project_key": project, "task_id": "task-2",
		"claude_acceptance": map[string]any{"outcome": "rejected", "send_backs": 2, "applied_as_is": false},
	})
	telemetryWrite(t, filepath.Join(tasks, "task-3.json"), map[string]any{"project_key": project, "task_id": "task-3"})
	telemetryWrite(t, filepath.Join(tasks, "task-4.json"), map[string]any{
		"project_key": project, "task_id": "task-4",
		"claude_acceptance": map[string]any{"outcome": "rejected", "send_backs": 5},
	})
	telemetryWrite(t, filepath.Join(state, "projects", "other-project", "telemetry", "tasks", "task-b.json"), map[string]any{
		"project_key": "other-project", "task_id": "task-b",
		"claude_acceptance": map[string]any{"outcome": "rejected", "send_backs": 9},
	})
	telemetryWrite(t, filepath.Join(state, "projects", project, "telemetry", "telemetry_health.json"), map[string]any{
		"components": map[string]any{"archive": map[string]any{"status": "OK", "consecutive_failures": 0, "lifetime_failures": 1, "last_checked_at": "2026-01-01T00:00:00Z"}},
	})
	telemetryWrite(t, filepath.Join(state, "telemetry", "telemetry_health.json"), map[string]any{
		"components": map[string]any{"retention": map[string]any{"status": "DEGRADED", "consecutive_failures": 2, "lifetime_failures": 3, "last_error_code": "LOCK_TIMEOUT"}},
	})
	return state, repo, project
}

func TestTelemetryCombinedReportAndReadOnlyInventory(t *testing.T) {
	state, repo, project := telemetryFixture(t)
	before := telemetryInventory(t, state, repo)
	var out bytes.Buffer
	if err := RunTelemetry(&out, state, repo, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Project key: " + project,
		"| task-1 | 2 | 0 |",
		"| task-2 | 1 | 2 |",
		"| task-3 | 0 | — |",
		"| task-4 | 0 | 5 |",
		"| task-5 | 1 | — |",
		"| raw-model | attested-raw | 0 | — |",
		"| provider-d | model-d | — | — | 0 |",
		"Duplicate archived jobs skipped: 1",
		"Unsupported legacy facts: 1",
		"Input status: PARTIAL",
		"unreadable job input",
		"| project | archive | OK |",
		"| root | retention | DEGRADED |",
		"LOCK_TIMEOUT",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"task-b", "other-project", "archive-wrong"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("report leaked %q\n%s", unwanted, text)
		}
	}
	after := telemetryInventory(t, state, repo)
	if len(before) != len(after) {
		t.Fatalf("inventory size changed: %d -> %d", len(before), len(after))
	}
	for path, digest := range before {
		if after[path] != digest {
			t.Errorf("file changed during read: %s", path)
		}
	}

	out.Reset()
	if err := RunTelemetry(&out, state, repo, true); err != nil {
		t.Fatal(err)
	}
	var decoded telemetryReport
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProjectKey != project || len(decoded.Tasks) != 5 || decoded.Coverage.DuplicateJobs != 1 {
		t.Fatalf("unexpected JSON report: %+v", decoded)
	}
}

func TestTelemetryMalformedInputReportsPartial(t *testing.T) {
	state, repo := t.TempDir(), t.TempDir()
	project := contextio.ProjectKey(repo)
	if err := os.MkdirAll(filepath.Join(state, "codex_exec_mcp", "jobs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "codex_exec_mcp", "jobs", "broken.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "telemetry"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "telemetry", "learning_summary.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "telemetry", "telemetry_health.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	telemetryWrite(t, filepath.Join(state, "projects", project, "telemetry", "tasks", "task-3.json"), map[string]any{
		"project_key": project, "task_id": "task-3",
		"claude_acceptance": map[string]any{"outcome": "rejected", "send_backs": 1},
	})
	var out bytes.Buffer
	if err := RunTelemetry(&out, state, repo, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"| task-3 | 0 | 1 |", "Input status: PARTIAL", "unreadable job input", "unreadable learning summary input", "malformed root health"} {
		if !strings.Contains(text, want) {
			t.Errorf("partial report missing %q\n%s", want, text)
		}
	}
}

func TestTelemetryLearningSummaryInputStatus(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		partial    bool
	}{
		{name: "missing"},
		{name: "read failure", partial: true},
		{name: "parse failure", data: "{", partial: true},
		{name: "invalid schema", data: `{}`, partial: true},
		{name: "invalid trailer", data: `{"schema_version":1,"sources":[],"records":[],"cohorts":{}} {}`, partial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, repo := t.TempDir(), t.TempDir()
			path := filepath.Join(state, "telemetry", "learning_summary.json")
			if tc.name == "read failure" {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if tc.data != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			report := collectTelemetry(state, contextio.ProjectKey(repo))
			text := renderTelemetry(report)
			if strings.Contains(text, "Input status: PARTIAL") != tc.partial ||
				strings.Contains(text, "unreadable learning summary input") != tc.partial {
				t.Fatalf("unexpected summary input status:\n%s", text)
			}
		})
	}
}

func TestTelemetryLegacyHealth(t *testing.T) {
	state, repo := t.TempDir(), t.TempDir()
	project := contextio.ProjectKey(repo)
	legacy := map[string]any{
		"failure_count": 21799, "last_error_code": "TELEMETRY_PROJECTION_FAILED",
		"last_projection_write_at": "2026-01-01T00:00:00Z", "status": "DEGRADED", "updated_at": "2026-01-02T00:00:00Z",
	}
	telemetryWrite(t, filepath.Join(state, "telemetry", "telemetry_health.json"), legacy)
	text := renderTelemetry(collectTelemetry(state, project))
	if !strings.Contains(text, "Historical unattributed") ||
		!strings.Contains(text, "| root | legacy (unattributed) | DEGRADED | — | — | 21799 | TELEMETRY_PROJECTION_FAILED | 2026-01-02T00:00:00Z |") {
		t.Fatalf("missing legacy root health:\n%s", text)
	}
	legacy["components"] = map[string]any{}
	telemetryWrite(t, filepath.Join(state, "projects", project, "telemetry", "telemetry_health.json"), legacy)
	report := collectTelemetry(state, project)
	if len(report.Health) != 2 || report.Health[0].Scope != "project" || report.Health[0].Historical == nil ||
		*report.Health[0].Historical != 21799 || report.Health[0].Consecutive != nil || report.Health[0].Lifetime != nil {
		t.Fatalf("unexpected legacy project health: %+v", report.Health)
	}
}

func TestTelemetryLegacyReviewerFacts(t *testing.T) {
	state, repo := t.TempDir(), t.TempDir()
	project := contextio.ProjectKey(repo)
	telemetryWrite(t, filepath.Join(state, "codex_exec_mcp", "jobs", telemetryID("a")+".json"), map[string]any{
		"jobId": telemetryID("a"), "taskId": "task-1", "project_key": project, "reviewer1Valid": 2,
	})
	telemetryWrite(t, filepath.Join(state, "telemetry", "learning_summary.json"), map[string]any{
		"schema_version": 1, "sources": []any{}, "cohorts": map[string]any{},
		"records": []any{
			map[string]any{"id": telemetryID("a"), "project_key": project},
			map[string]any{"id": telemetryID("b"), "project_key": project, "review_verdict": "FAIL", "reviewer1_false": 1, "reviewer1_false_present": true},
			map[string]any{"id": telemetryID("c"), "project_key": "other-project"},
		},
	})
	report := collectTelemetry(state, project)
	if report.Coverage.Legacy != 2 || report.Coverage.DuplicateJobs != 1 || len(report.Jobs) != 2 {
		t.Fatalf("unexpected legacy coverage: %+v", report.Coverage)
	}
}

func telemetryInventory(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			out[path] = string(sum[:])
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}
