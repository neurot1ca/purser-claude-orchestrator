package enforce

import (
	"strings"

	"purser-hook/internal/models"
	"purser-hook/internal/policy"
)

var researchCapabilities = models.CodexResearchCapabilities

func resolveResearch(config map[string]any, requested string, basis string) policy.RouteResolution {
	entry := mapValue(mapValue(config["routes"])["research"])
	if requested == "" {
		requested = stringValue(entry["effort"])
	}
	if requested == "ultra" {
		return policy.RouteResolution{Code: "INVALID_EFFORT", Message: "Research effort ultra is not allowed.", Route: "research"}
	}
	if (requested == "xhigh" || requested == "max") && !strings.HasPrefix(basis, "measured:") {
		return policy.RouteResolution{Code: "RESEARCH_EFFORT_REQUIRES_MEASUREMENT", Message: "Research effort requires measured evidence.", Route: "research"}
	}
	if config["coder_agent"] != "codex" {
		return policy.ResolveResearch(config, requested, false)
	}
	primary := stringValue(entry["model"])
	fallback := policy.ResolveResearch(config, requested, true)
	if !fallback.Valid {
		return fallback
	}
	result := researchCapabilities([]models.CodexCapabilityCandidate{{Model: primary, Effort: requested}, {Model: *fallback.Model, Effort: *fallback.Effort}})
	if result.Unknown || len(result.Candidates) != 2 {
		return policy.RouteResolution{Code: "RESEARCH_MODEL_UNVERIFIED", Message: "Research model availability could not be verified.", Route: "research"}
	}
	switch result.Candidates[0].Status {
	case models.CodexCapabilityAvailable:
		return policy.ResolveResearch(config, requested, false)
	case models.CodexCapabilityUnsupportedEffort:
		return policy.RouteResolution{Code: "INVALID_EFFORT", Message: "Research effort is unsupported by the configured model.", Route: "research"}
	case models.CodexCapabilityAbsent:
		if result.Candidates[1].Status == models.CodexCapabilityAvailable {
			return fallback
		}
	}
	return policy.RouteResolution{Code: "RESEARCH_MODEL_UNVERIFIED", Message: "Research model availability could not be verified.", Route: "research"}
}
