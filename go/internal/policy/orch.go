package policy

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	orchValid              = "VALID"
	orchMissingLine        = "MISSING_ORCH_LINE"
	orchMalformedLine      = "MALFORMED_ORCH_LINE"
	orchUnknownKey         = "UNKNOWN_KEY"
	orchMissingRequiredKey = "MISSING_REQUIRED_KEY"
	orchInvalidEnum        = "INVALID_ENUM_VALUE"
	orchNeedsBasis         = "SOL_ROUTE_NEEDS_BASIS"
	orchInvalidScope       = "INVALID_SCOPE"
	orchUnboundedScope     = "UNBOUNDED_SCOPE"
	orchConfigInvalid      = "CONFIG_INVALID"
)

var (
	orchKeys        = map[string]bool{"task": true, "phase": true, "route": true, "reason": true, "basis": true, "scope": true, "eta": true, "review_findings": true, "requested": true}
	orchDrivePrefix = regexp.MustCompile(`^[A-Za-z]:`)
	solRoutes       = map[string]bool{"normal": true, "consequential": true, "escalated": true}
	reviewFindingID = regexp.MustCompile(`^F[0-9a-f]{16}-[0-9]{4}$`)
)

type OrchParseResult struct {
	Valid          bool     `json:"valid"`
	Code           string   `json:"code"`
	Message        string   `json:"message"`
	Prompt         string   `json:"prompt"`
	TaskID         *string  `json:"task_id"`
	Phase          *string  `json:"phase"`
	Route          *string  `json:"route"`
	Reason         *string  `json:"reason"`
	Basis          *string  `json:"basis"`
	Requested      *string  `json:"requested"`
	Scope          []string `json:"scope"`
	ETAMinutes     *int     `json:"eta_minutes"`
	ReviewFindings []string `json:"review_findings,omitempty"`
	Normalizations []string `json:"normalizations,omitempty"`
	Correction     string   `json:"-"`
}

func ParseOrchPrompt(prompt any, config map[string]any) OrchParseResult {
	text, isString := prompt.(string)
	if !isString {
		return orchFailure("", orchMissingLine, "The prompt must start with an ORCH line.", nil)
	}
	first, remainder, separated := strings.Cut(text, "\n")
	if strings.HasSuffix(first, "\r") {
		first = strings.TrimSuffix(first, "\r")
	}
	if !separated {
		remainder = ""
	}
	if !strings.HasPrefix(first, "ORCH") {
		return orchFailure(text, orchMissingLine, "The prompt must start with an ORCH line.", nil)
	}
	if first != "ORCH" && !strings.HasPrefix(first, "ORCH|") {
		return orchFailure(remainder, orchMalformedLine, "The ORCH line has an invalid prefix.", nil)
	}

	values := make(map[string]string)
	normalizations := []string{}
	for _, field := range strings.Split(first, "|")[1:] {
		key, value, hasValue := strings.Cut(field, "=")
		if !hasValue {
			if field == "eta" {
				return orchFailure(remainder, orchMalformedLine, "ORCH eta must use eta=<N>m with N an integer from 1..600.", values)
			}
			return orchFailure(remainder, orchMalformedLine, "Each ORCH field must use key=value syntax.", values)
		}
		if key == "" {
			return orchFailure(remainder, orchMalformedLine, "An ORCH field has an empty key.", values)
		}
		if !orchKeys[key] {
			return orchFailure(remainder, orchUnknownKey, fmt.Sprintf("Unknown ORCH key %s.", pythonRepr(key)), values)
		}
		if _, exists := values[key]; exists {
			return orchFailure(remainder, orchMalformedLine, fmt.Sprintf("ORCH key %s occurs more than once.", pythonRepr(key)), values)
		}
		if key == "task" && strings.TrimSpace(value) == "" {
			return orchFailure(remainder, orchMalformedLine, "The ORCH task value cannot be empty.", values)
		}
		if key == "phase" || key == "reason" {
			if normalized := normalizeOrchEnum(key, value); normalized != value {
				normalizations = append(normalizations, key+"="+value+"->"+normalized)
				value = normalized
			}
		}
		values[key] = value
	}

	etaMinutes := (*int)(nil)
	if eta, present := values["eta"]; present {
		if len(eta) < 2 || eta[len(eta)-1] != 'm' || eta[0] < '1' || eta[0] > '9' {
			return orchFailure(remainder, orchMalformedLine, "ORCH eta must use eta=<N>m with N an integer from 1..600.", values)
		}
		for _, digit := range eta[:len(eta)-1] {
			if digit < '0' || digit > '9' {
				return orchFailure(remainder, orchMalformedLine, "ORCH eta must use eta=<N>m with N an integer from 1..600.", values)
			}
		}
		if len(eta[:len(eta)-1]) > 3 {
			return orchFailure(remainder, orchMalformedLine, "ORCH eta must use eta=<N>m with N an integer from 1..600.", values)
		}
		minutes := 0
		for _, digit := range eta[:len(eta)-1] {
			minutes = minutes*10 + int(digit-'0')
		}
		if minutes > 600 {
			return orchFailure(remainder, orchMalformedLine, "ORCH eta must use eta=<N>m with N an integer from 1..600.", values)
		}
		etaMinutes = &minutes
	}

	for _, key := range []string{"task", "phase", "route"} {
		if _, present := values[key]; !present {
			return orchFailure(remainder, orchMissingRequiredKey, fmt.Sprintf("Missing required ORCH key %s.", pythonRepr(key)), values)
		}
	}
	if err := ValidateConfig(config); err != nil {
		message := err.Error()
		if _, detail, found := strings.Cut(message, ": "); found {
			message = detail
		}
		return orchFailure(remainder, orchConfigInvalid, "Policy configuration is invalid: "+message, values)
	}

	allowed := map[string]map[string]bool{}
	for _, key := range []string{"phase", "reason"} {
		set := make(map[string]bool)
		if items, ok := config[map[string]string{"phase": "allowed_phases", "reason": "allowed_reasons"}[key]].([]any); ok {
			for _, item := range items {
				if value, ok := item.(string); ok {
					set[value] = true
				}
			}
		}
		allowed[key] = set
	}
	routes, _ := config["routes"].(map[string]any)
	allowed["route"] = make(map[string]bool, len(routes))
	for route := range routes {
		allowed["route"][route] = true
	}
	for _, key := range []string{"phase", "reason", "route"} {
		value, present := values[key]
		if present && !allowed[key][value] {
			return orchFailure(remainder, orchInvalidEnum, fmt.Sprintf("ORCH %s value %s is not allowed.", key, pythonRepr(value)), values)
		}
	}
	if requested, present := values["requested"]; present && requested != "user" {
		return orchFailure(remainder, orchInvalidEnum, "ORCH requested value must be user.", values)
	}

	scope := []string{}
	if values["phase"] == "implementation" || values["phase"] == "plan" {
		if values["phase"] == "implementation" {
			if _, present := values["scope"]; !present {
				return orchFailure(remainder, orchMissingRequiredKey, "Missing required ORCH key 'scope' for implementation phase.", values)
			}
		}
		if raw, present := values["scope"]; present {
			normalized, code := implementationScope(raw)
			if code != "" {
				message := "Scope must contain repository-relative paths without traversal."
				if code == orchUnboundedScope {
					message = "Scope cannot resolve to the whole repository."
				}
				return orchFailure(remainder, code, message, values)
			}
			scope = normalized
		}
	}

	if (values["phase"] == "plan") != (values["route"] == "plan_standard") {
		return orchFailure(remainder, orchInvalidEnum, "The valid plan pairing is phase=plan with route=plan_standard; phase=plan cannot use other routes and route=plan_standard cannot use other phases.", values)
	}
	if values["route"] == "research" && values["phase"] != "operation" {
		return orchFailure(remainder, orchInvalidEnum, "route=research requires phase=operation.", values)
	}
	if solRoutes[values["route"]] && strings.TrimSpace(values["basis"]) == "" && values["requested"] != "user" {
		route := values["route"]
		message := fmt.Sprintf("route=%s runs the Sol model. Add basis=<the design choice left to the worker> (for escalated: the concrete miss). If the prompt already states the decisions, use route=subtle instead.", route)
		if agent, _ := config["coder_agent"].(string); agent == "opencode" || agent == "agy" || agent == "claude" {
			message = fmt.Sprintf("route=%s requires basis=<the design choice left to the worker> (for escalated: the concrete miss).", route)
		}
		return orchFailure(remainder, orchNeedsBasis, message, values)
	}
	reviewFindings := []string{}
	if raw, present := values["review_findings"]; present {
		if values["reason"] != "fix" {
			return orchFailure(remainder, orchMalformedLine, "ORCH review_findings may only accompany reason=fix.", values)
		}
		seen := map[string]bool{}
		for _, reference := range strings.Split(raw, ",") {
			if !reviewFindingID.MatchString(reference) || seen[reference] || len(seen) == 20 {
				return orchFailure(remainder, orchMalformedLine, "ORCH review_findings must be unique, comma-separated Reviewer finding references.", values)
			}
			seen[reference] = true
			reviewFindings = append(reviewFindings, reference)
		}
	}

	return OrchParseResult{
		Valid: true, Code: orchValid, Message: "ORCH metadata is valid.", Prompt: remainder,
		TaskID: stringPointer(values["task"]), Phase: stringPointer(values["phase"]), Route: stringPointer(values["route"]),
		Reason: optionalStringPointer(values, "reason"), Basis: optionalStringPointer(values, "basis"),
		Requested: optionalStringPointer(values, "requested"),
		Scope:     scope, ETAMinutes: etaMinutes, ReviewFindings: reviewFindings, Normalizations: normalizations,
	}
}

func orchFailure(prompt, code, message string, values map[string]string) OrchParseResult {
	correction := ""
	if values != nil && ((code == orchInvalidEnum && strings.HasPrefix(message, "ORCH ")) || (code == orchMissingRequiredKey && strings.Contains(message, "scope")) || code == orchNeedsBasis) {
		correction = correctedOrchLine(values)
	}
	result := OrchParseResult{Valid: false, Code: code, Message: message, Prompt: prompt, Scope: []string{}, Correction: correction}
	if values != nil {
		result.TaskID = optionalStringPointer(values, "task")
		result.Phase = optionalStringPointer(values, "phase")
		result.Route = optionalStringPointer(values, "route")
		result.Reason = optionalStringPointer(values, "reason")
		result.Basis = optionalStringPointer(values, "basis")
		result.Requested = optionalStringPointer(values, "requested")
	}
	return result
}

func (result OrchParseResult) DenialMessage() string {
	if result.Correction == "" {
		return result.Message
	}
	return result.Message + "\nCorrected: " + result.Correction
}

func normalizeOrchEnum(key, value string) string {
	normalized := strings.ToLower(strings.ReplaceAll(value, "-", "_"))
	aliases := map[string]map[string]string{
		"phase":  {"implement": "implementation", "impl": "implementation", "code": "implementation", "op": "operation", "ops": "operation"},
		"reason": {"feedback": "fix", "review_feedback": "fix", "review": "fix", "sendback": "fix", "send_back": "fix", "new": "new_task", "cont": "continue"},
	}
	if canonical, ok := aliases[key][normalized]; ok {
		return canonical
	}
	canonicalValues := map[string]map[string]bool{
		"phase":  {"implementation": true, "operation": true, "plan": true},
		"reason": {"new_task": true, "continue": true, "fix": true, "route_change": true, "reanchor": true, "repeated_failure": true, "user_override": true},
	}
	if canonicalValues[key][normalized] {
		return normalized
	}
	return value
}

func correctedOrchLine(values map[string]string) string {
	corrected := make(map[string]string, len(values)+2)
	for key, value := range values {
		corrected[key] = value
	}
	for _, key := range []string{"phase", "reason", "route", "requested"} {
		if value, ok := corrected[key]; ok {
			if candidate := closestOrchEnum(key, value); candidate != "" {
				corrected[key] = candidate
			}
		}
	}
	if corrected["phase"] == "implementation" && corrected["scope"] == "" {
		corrected["scope"] = "<paths>"
	}
	if _, ok := corrected["basis"]; !ok && (corrected["route"] == "normal" || corrected["route"] == "consequential" || corrected["route"] == "escalated") {
		corrected["basis"] = "design:<...>"
	}
	keys := []string{"task", "phase", "route", "reason", "basis", "scope", "eta", "review_findings", "requested"}
	line := "ORCH"
	for _, key := range keys {
		if value, ok := corrected[key]; ok {
			line += "|" + key + "=" + value
		}
	}
	return line
}

func closestOrchEnum(key, value string) string {
	var candidates []string
	switch key {
	case "phase":
		candidates = []string{"implementation", "operation", "plan"}
	case "reason":
		candidates = []string{"new_task", "continue", "fix", "route_change", "reanchor", "repeated_failure", "user_override"}
	case "route":
		candidates = []string{"mechanical", "subtle", "normal", "consequential", "escalated", "operation", "plan_standard", "research"}
	case "requested":
		candidates = []string{"user"}
	default:
		return ""
	}
	best, distance, tied := "", len(value)+1, false
	for _, candidate := range candidates {
		d := orchEditDistance(strings.ToLower(strings.ReplaceAll(value, "-", "_")), candidate)
		if d < distance {
			best, distance, tied = candidate, d, false
		} else if d == distance {
			tied = true
		}
	}
	if distance <= 3 && !tied {
		return best
	}
	return ""
}

func orchEditDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, left := range a {
		current := make([]int, len(b)+1)
		current[0] = i + 1
		for j, right := range b {
			cost := 0
			if left != right {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}

func implementationScope(value string) ([]string, string) {
	parts := strings.Split(value, ",")
	unique := make(map[string]bool, len(parts))
	for _, raw := range parts {
		if strings.TrimSpace(raw) == "" {
			return nil, orchInvalidScope
		}
		item := strings.ReplaceAll(strings.TrimSpace(raw), `\`, "/")
		if strings.ContainsRune(item, '\x00') || strings.HasPrefix(item, "/") || torchDrive(item) {
			return nil, orchInvalidScope
		}
		for _, component := range strings.Split(item, "/") {
			if component == ".." {
				return nil, orchInvalidScope
			}
		}
		canonical := path.Clean(item)
		if canonical == "." || canonical == "" {
			return nil, orchUnboundedScope
		}
		unique[canonical] = true
	}
	result := make([]string, 0, len(unique))
	for item := range unique {
		result = append(result, item)
	}
	sort.Strings(result)
	return result, ""
}

func torchDrive(value string) bool { return orchDrivePrefix.MatchString(value) }

func optionalStringPointer(values map[string]string, key string) *string {
	value, ok := values[key]
	if !ok {
		return nil
	}
	return &value
}

func stringPointer(value string) *string { return &value }
