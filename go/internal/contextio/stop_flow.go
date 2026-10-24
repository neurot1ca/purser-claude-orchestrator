package contextio

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var transcriptTaskResultID = regexp.MustCompile(`(?i)\btask\s+#([a-z0-9_-]+)\b`)

func openTranscriptTasks(records []map[string]any) []string {
	type task struct{ title, status string }
	todoTasks, taskTasks := map[string]task{}, map[string]task{}
	createUses := map[string]string{}
	latestTodo, latestTask, sequence := -1, -1, 0
	for _, record := range records {
		for _, raw := range transcriptBlocks(record) {
			sequence++
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "tool_result" {
				useID := str(block["tool_use_id"])
				key, exists := createUses[useID]
				if !exists {
					continue
				}
				if id := transcriptTaskResultIDFor(block); id != "" {
					if item, exists := taskTasks[key]; exists {
						delete(taskTasks, key)
						taskTasks[id] = item
						createUses[useID] = id
					}
				}
				continue
			}
			if transcriptRecordType(record) != "assistant" || block["type"] != "tool_use" {
				continue
			}
			input, ok := block["input"].(map[string]any)
			if !ok {
				continue
			}
			switch shortStopToolName(str(block["name"])) {
			case "TodoWrite":
				latestTodo = sequence
				todoTasks = map[string]task{}
				todos, _ := input["todos"].([]any)
				for i, rawTodo := range todos {
					todo, ok := rawTodo.(map[string]any)
					if !ok {
						continue
					}
					title, status := str(todo["content"]), str(todo["status"])
					if title != "" && validTranscriptTaskStatus(status) {
						todoTasks[fmt.Sprintf("todo:%d", i)] = task{title, status}
					}
				}
			case "TaskCreate":
				latestTask = sequence
				title := str(input["subject"])
				status := "pending"
				if title == "" {
					continue
				}
				id := str(input["taskId"])
				if id == "" {
					if useID := str(block["id"]); useID != "" {
						id = "create:" + useID
						createUses[useID] = id
					} else {
						id = "subject:" + title
					}
				}
				taskTasks[id] = task{title, status}
			case "TaskUpdate":
				latestTask = sequence
				id, status := str(input["taskId"]), str(input["status"])
				if id == "" {
					continue
				}
				if status == "deleted" {
					delete(taskTasks, id)
					continue
				}
				if !validTranscriptTaskStatus(status) {
					continue
				}
				if existing, exists := taskTasks[id]; exists {
					existing.status = status
					taskTasks[id] = existing
				}
			}
		}
	}
	tasks := taskTasks
	if latestTodo > latestTask {
		tasks = todoTasks
	}
	var titles []string
	for _, item := range tasks {
		if item.status == "pending" || item.status == "in_progress" {
			titles = append(titles, item.title)
		}
	}
	sort.Strings(titles)
	return titles
}

func transcriptTaskResultIDFor(result map[string]any) string {
	for _, key := range []string{"taskId", "task_id"} {
		if id := str(result[key]); id != "" {
			return id
		}
	}
	if structured, ok := result["structuredContent"].(map[string]any); ok {
		for _, key := range []string{"taskId", "task_id", "id"} {
			if id := str(structured[key]); id != "" {
				return id
			}
		}
	}
	text := transcriptText(result["content"])
	if match := transcriptTaskResultID.FindStringSubmatch(text); len(match) == 2 {
		return match[1]
	}
	return ""
}

func validTranscriptTaskStatus(status string) bool {
	switch status {
	case "pending", "in_progress", "completed":
		return true
	default:
		return false
	}
}

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
