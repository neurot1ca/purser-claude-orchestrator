package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const artifactTestJob = "artifact-test-job"

const artifactTestPlan = "# Summary: Plan\n## Goal and boundaries\n## Current evidence\n## Decision\n## Milestones\n### M1 - first\n**Dependency:** none\n**Write paths:**\n- first.go\n**Implementation:** first milestone\n### M2 - second\n**Dependency:** M1\n**Write paths:**\n- second.go\n**Implementation:** second milestone\n## Risks/open decisions\n## Implementation handoff\n"

func setupArtifactCall(t *testing.T, planPath string) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	if planPath == ":inside" {
		planPath = filepath.Join(root, "plans", "plan.md")
		if err := os.MkdirAll(filepath.Dir(planPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(planPath, []byte(artifactTestPlan), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"codex_exec_mcp/jobs", "async_jobs", "bundle_artifacts"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	job := []byte(`{"jobId":"` + artifactTestJob + `","state":"SUCCEEDED","decisionRecorded":false}`)
	if err := os.WriteFile(filepath.Join(root, "codex_exec_mcp", "jobs", artifactTestJob+".json"), job, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "async_jobs", artifactTestJob+".json"), []byte(`{"call_key":"artifact-call","stage":"CONSUMED"}`), 0600); err != nil {
		t.Fatal(err)
	}
	bundle, _ := json.Marshal(map[string]any{"plan_path": planPath})
	if err := os.WriteFile(filepath.Join(root, "bundle_artifacts", "artifact-call.json"), bundle, 0600); err != nil {
		t.Fatal(err)
	}
	return root, job
}

func callArtifact(root, name string) map[string]any {
	result, _ := dispatchOperation(context.Background(), "codex_result", map[string]any{"jobId": artifactTestJob, "artifact": name}, root)
	return result
}

func artifactText(result map[string]any) string {
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text
}

func TestCodexResultArtifactReturnsCompletePlan(t *testing.T) {
	state, _ := setupArtifactCall(t, ":inside")
	if got := artifactText(callArtifact(state, "plan")); got != artifactTestPlan {
		t.Fatalf("plan artifact mismatch: %q", got)
	}
}

func TestCodexResultArtifactReturnsMilestoneSection(t *testing.T) {
	state, _ := setupArtifactCall(t, ":inside")
	got := artifactText(callArtifact(state, "M2"))
	if !strings.HasPrefix(got, "### M2 - second") || strings.Contains(got, "### M1") || !strings.Contains(got, "**Implementation:** second milestone") {
		t.Fatalf("unexpected milestone section: %q", got)
	}
}

func TestCodexResultArtifactRejectsUnknownMilestone(t *testing.T) {
	state, _ := setupArtifactCall(t, ":inside")
	result := callArtifact(state, "M9")
	if result["isError"] != true || !strings.Contains(artifactText(result), "valid values: plan, M1, M2") {
		t.Fatalf("unknown milestone was not rejected with valid values: %#v", result)
	}
}

func TestCodexResultArtifactRejectsJobWithoutPlan(t *testing.T) {
	state, _ := setupArtifactCall(t, "")
	result := callArtifact(state, "plan")
	if result["isError"] != true || !strings.Contains(artifactText(result), "valid values: none") {
		t.Fatalf("job without plan was not rejected: %#v", result)
	}
}

func TestCodexResultArtifactRequiresFinalization(t *testing.T) {
	state, _ := setupArtifactCall(t, "")
	if err := os.WriteFile(filepath.Join(state, "async_jobs", artifactTestJob+".json"), []byte(`{"call_key":"artifact-call","stage":"FINISHED"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(state, "bundle_artifacts", "artifact-call.json")); err != nil {
		t.Fatal(err)
	}
	result := callArtifact(state, "plan")
	if want := "Call codex_result without artifact first to finalize this job, then request the plan artifact."; result["isError"] != true || artifactText(result) != want {
		t.Fatalf("unfinalized artifact response: %#v; want %q", result, want)
	}
}

func TestCodexResultArtifactRejectsPathOutsidePlansDirectory(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte(artifactTestPlan), 0600); err != nil {
		t.Fatal(err)
	}
	state, _ := setupArtifactCall(t, outside)
	result := callArtifact(state, "plan")
	if result["isError"] != true || !strings.Contains(artifactText(result), "outside the state root plans directory") {
		t.Fatalf("outside plan path was not rejected: %#v", result)
	}
}

func TestCodexResultArtifactDoesNotChangeJobState(t *testing.T) {
	state, before := setupArtifactCall(t, ":inside")
	jobPath := filepath.Join(state, "codex_exec_mcp", "jobs", artifactTestJob+".json")
	if result := callArtifact(state, "plan"); result["isError"] == true {
		t.Fatalf("artifact read failed: %#v", result)
	}
	after, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("artifact read changed job state: before=%s after=%s", before, after)
	}
}
