package enforce

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"purser-hook/internal/models"
	"purser-hook/internal/policy"
)

func TestResearchAdmission(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".claude", "hooks", "orchestrator", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		t.Fatal(err)
	}
	researchEntry := mapValue(mapValue(config["routes"])["research"])
	primary := stringValue(researchEntry["model"])
	scoped := mapValue(researchEntry["scoped_fallback"])
	synthesis := mapValue(researchEntry["synthesis_fallback"])
	operation := mapValue(mapValue(config["routes"])["operation"])
	operationModel, operationEffort := stringValue(operation["model"]), stringValue(operation["effort"])
	config["routes"].(map[string]any)["operation"] = map[string]any{"model": "operation-model", "effort": "high"}
	config["coder_agent"] = "codex"
	if primary == "" || scoped == nil || synthesis == nil {
		t.Fatal("shipped research route is incomplete")
	}
	if operationModel == "" || operationEffort == "" {
		t.Fatal("shipped operation route is incomplete")
	}
	old := researchCapabilities
	t.Cleanup(func() { researchCapabilities = old })
	calls := 0
	researchCapabilities = func(candidates []models.CodexCapabilityCandidate) models.CodexCapabilityResult {
		calls++
		result := models.CodexCapabilityResult{Candidates: make([]models.CodexCapability, len(candidates))}
		for i, candidate := range candidates {
			status := models.CodexCapabilityAbsent
			if candidate.Model == primary && candidate.Effort != "ultra" {
				status = models.CodexCapabilityAvailable
			}
			result.Candidates[i] = models.CodexCapability{Candidate: candidate, Status: status}
		}
		return result
	}
	if got := resolveResearch(config, "", ""); !got.Valid || *got.Model != primary || *got.Effort != "medium" {
		t.Fatalf("default selection = %#v", got)
	}
	if got := resolveResearch(config, "high", ""); !got.Valid || *got.Model != primary || *got.Effort != "high" {
		t.Fatalf("high selection = %#v", got)
	}
	if calls != 2 {
		t.Fatalf("capability listings = %d, want one per admission", calls)
	}

	researchCapabilities = func([]models.CodexCapabilityCandidate) models.CodexCapabilityResult {
		return models.CodexCapabilityResult{Unknown: true}
	}
	if got := resolveResearch(config, "medium", ""); got.Code != "RESEARCH_MODEL_UNVERIFIED" {
		t.Fatalf("unknown listing = %#v", got)
	}
	researchCapabilities = func(candidates []models.CodexCapabilityCandidate) models.CodexCapabilityResult {
		return models.CodexCapabilityResult{Candidates: []models.CodexCapability{
			{Candidate: candidates[0], Status: models.CodexCapabilityAbsent},
			{Candidate: candidates[1], Status: models.CodexCapabilityAvailable},
		}}
	}
	if got := resolveResearch(config, "medium", ""); !got.Valid || *got.Model != "gpt-6-luna" {
		t.Fatalf("fallback selection = %#v", got)
	}
	researchCapabilities = func(candidates []models.CodexCapabilityCandidate) models.CodexCapabilityResult {
		return models.CodexCapabilityResult{Candidates: []models.CodexCapability{
			{Candidate: candidates[0], Status: models.CodexCapabilityUnsupportedEffort},
			{Candidate: candidates[1], Status: models.CodexCapabilityAvailable},
		}}
	}
	if got := resolveResearch(config, "high", ""); got.Code != "INVALID_EFFORT" {
		t.Fatalf("unsupported effort = %#v", got)
	}
	for effort, basis := range map[string]string{"ultra": "measured:run", "xhigh": "", "max": "estimate"} {
		if got := resolveResearch(config, effort, basis); got.Valid {
			t.Fatalf("ungated effort %s selected: %#v", effort, got)
		}
	}
	researchCapabilities = func(candidates []models.CodexCapabilityCandidate) models.CodexCapabilityResult {
		return models.CodexCapabilityResult{Candidates: []models.CodexCapability{
			{Candidate: candidates[0], Status: models.CodexCapabilityAvailable},
			{Candidate: candidates[1], Status: models.CodexCapabilityAbsent},
		}}
	}
	if got := resolveResearch(config, "xhigh", "measured:run"); !got.Valid || *got.Effort != "xhigh" {
		t.Fatalf("measured xhigh = %#v", got)
	}

	config["coder_agent"] = "agy"
	listingCalls := calls
	config["coder_agent"] = "opencode"
	for name, raw := range config["routes"].(map[string]any) {
		if name == "research" {
			continue
		}
		entry := mapValue(raw)
		entry["model"], entry["effort"] = "openai/model", "none"
	}
	if got := resolveResearch(config, "high", ""); !got.Valid || *got.Model != "openai/model" || *got.Effort != "none" {
		t.Fatalf("non-Codex selection = %#v", got)
	}
	if calls != listingCalls {
		t.Fatal("non-Codex research listed Codex models")
	}
	if err := validateObservationValue("route", "research"); err != nil {
		t.Fatalf("research route telemetry rejected: %v", err)
	}
	if err := validateObservationValue("served_route", "research"); err != nil {
		t.Fatalf("served research route telemetry rejected: %v", err)
	}
	if err := validateObservationValue("effort", "max"); err != nil {
		t.Fatalf("research effort telemetry rejected: %v", err)
	}
	thread := map[string]any{"task_id": "research-task", "route": "research", "model": primary, "effort": "high"}
	if got := policy.ThreadCompatibility(thread, thread); !got.Compatible {
		t.Fatalf("research reply identity = %#v", got)
	}
	before := calls
	if got := policy.ResolveRoute("normal", config, false); !got.Valid {
		t.Fatalf("ordinary route = %#v", got)
	}
	if calls != before {
		t.Fatal("ordinary job requested research capability listing")
	}
}
