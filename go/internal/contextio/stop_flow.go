package contextio

import "strings"

func assistantEndsWithQuestion(message string) bool {
	trimmed := strings.TrimRightFunc(message, isPythonWhitespace)
	return strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, "؟") || strings.HasSuffix(trimmed, "？")
}

func planCodexDispatched(records []map[string]any, latestUser int, planID string) bool {
	if latestUser < 0 || latestUser >= len(records) || planID == "" {
		return false
	}
	for _, record := range records[latestUser+1:] {
		for _, raw := range transcriptBlocks(record) {
			block, ok := raw.(map[string]any)
			if !ok || block["type"] != "tool_use" {
				continue
			}
			switch shortStopToolName(str(block["name"])) {
			case "codex_start":
				if planTaskInDispatchInput(block["input"], planID) {
					return true
				}
			case "Agent":
				input, ok := block["input"].(map[string]any)
				if ok && input["subagent_type"] == "purser:dispatcher" && planTaskInDispatchInput(input, planID) {
					return true
				}
			}
		}
	}
	return false
}

func planMilestoneDispatched(records []map[string]any, latestUser int, planID string, milestone int) bool {
	if latestUser < 0 || latestUser >= len(records) || planID == "" || milestone <= 0 {
		return false
	}
	for _, record := range records[latestUser+1:] {
		for _, raw := range transcriptBlocks(record) {
			block, ok := raw.(map[string]any)
			if !ok || block["type"] != "tool_use" {
				continue
			}
			dispatcher := false
			switch shortStopToolName(str(block["name"])) {
			case "codex_start":
				dispatcher = true
			case "Agent":
				input, ok := block["input"].(map[string]any)
				dispatcher = ok && input["subagent_type"] == "purser:dispatcher"
			}
			if dispatcher && planMilestoneInDispatchInput(block["input"], planID, milestone) {
				return true
			}
		}
	}
	return false
}

func planMilestoneInDispatchInput(value any, planID string, milestone int) bool {
	switch input := value.(type) {
	case map[string]any:
		for _, child := range input {
			if planMilestoneInDispatchInput(child, planID, milestone) {
				return true
			}
		}
	case []any:
		for _, child := range input {
			if planMilestoneInDispatchInput(child, planID, milestone) {
				return true
			}
		}
	case string:
		if n, ok := planTaskMilestone(planID, input); ok && n == milestone {
			return true
		}
		for _, marker := range strings.Split(input, "ORCH|task=")[1:] {
			fields := strings.FieldsFunc(marker, func(r rune) bool { return r == '|' || r == ' ' || r == '\t' || r == '\r' || r == '\n' })
			if len(fields) > 0 {
				if n, ok := planTaskMilestone(planID, fields[0]); ok && n == milestone {
					return true
				}
			}
		}
	}
	return false
}

func planTaskInDispatchInput(value any, planID string) bool {
	switch input := value.(type) {
	case map[string]any:
		for key, child := range input {
			if key == "task_id" || key == "taskId" {
				if taskID, ok := child.(string); ok && PlanTaskMatches(planID, taskID) {
					return true
				}
			}
			if planTaskInDispatchInput(child, planID) {
				return true
			}
		}
	case []any:
		for _, child := range input {
			if planTaskInDispatchInput(child, planID) {
				return true
			}
		}
	case string:
		if PlanTaskMatches(planID, input) {
			return true
		}
		for _, marker := range strings.Split(input, "ORCH|task=")[1:] {
			fields := strings.FieldsFunc(marker, func(r rune) bool { return r == '|' || r == ' ' || r == '\t' || r == '\r' || r == '\n' })
			if len(fields) == 0 {
				continue
			}
			taskID := fields[0]
			if PlanTaskMatches(planID, taskID) {
				return true
			}
		}
	}
	return false
}
