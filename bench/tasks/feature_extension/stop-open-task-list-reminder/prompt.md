# Remind at Stop when the transcript task list still has open items

## Problem

In `go/internal/contextio/stop.go` and `go/internal/contextio/stop_flow.go`, `Stop` checks for ready plan passes and pending notices, but does not inspect the session transcript's `TodoWrite` or `TaskCreate`/`TaskUpdate` tool records. If the assistant attempts to stop while task list items remain `pending` or `in_progress`, `Stop` allows the turn to end without reminding the assistant to finish or update the open items.

## Required Behavior

1. **Reconstruct open transcript tasks (`openTranscriptTasks` in `go/internal/contextio/stop_flow.go`):**
   - Scan `transcriptBlocks(record)` in order across `records`, tracking a monotonically increasing block sequence number:
     - For `assistant` records (`transcriptRecordType(record) == "assistant"`) with `"type": "tool_use"` and a map `"input"`:
       - When `shortStopToolName(str(block["name"])) == "TodoWrite"`: record the sequence number as the latest `TodoWrite` snapshot, replace the `TodoWrite` task map with the items from `input["todos"]` (`[]any`), and keep each item whose `"content"` is non-empty and whose `"status"` is `"pending"`, `"in_progress"`, or `"completed"`.
       - When `shortStopToolName(str(block["name"])) == "TaskCreate"`: record the sequence number as the latest task-tool event. If `input["subject"]` is non-empty, add a task with title `str(input["subject"])` and status `"pending"`. Key it by `str(input["taskId"])` if non-empty; otherwise if `str(block["id"])` is non-empty, key it by `"create:" + useID` and remember `useID -> key` so a subsequent `tool_result` can re-key it; otherwise key it by `"subject:" + title`.
       - When `shortStopToolName(str(block["name"])) == "TaskUpdate"`: record the sequence number as the latest task-tool event. For a non-empty `id := str(input["taskId"])`: if `status := str(input["status"])` is `"deleted"`, delete `id` from the task-tool map; otherwise if `status` is `"pending"`, `"in_progress"`, or `"completed"` and `id` exists in the task-tool map, update its status.
     - For `"type": "tool_result"` blocks whose `"tool_use_id"` matches a remembered `TaskCreate` call: extract the created task ID from `result["taskId"]`, `result["task_id"]`, `result["structuredContent"]` (`taskId`, `task_id`, or `id`), or a regex match `(?i)\btask\s+#([a-z0-9_-]+)\b` on `transcriptText(result["content"])`. When found, move the task entry in the task-tool map to that resolved ID.
   - Select the `TodoWrite` map if the latest `TodoWrite` sequence number is greater than the latest `TaskCreate`/`TaskUpdate` sequence number; otherwise select the `TaskCreate`/`TaskUpdate` map.
   - Collect and return the sorted titles of all tasks in the selected map whose status is `"pending"` or `"in_progress"`.

2. **Emit a deduplicated reminder in `Stop` (`go/internal/contextio/stop.go`):**
   - When `latestUser >= 0 && !questionTurn && planReason == ""`, call `openTranscriptTasks(records)`.
   - If open titles exist, let `openSet := strings.Join(openTitles, "; ")`.
   - Check `state["todos_reminded"]` (a map with `"user_index"` and `"open_set"`). Unless `user_index` equals `latestUser` and `open_set` equals `openSet`:
     - Update `state["todos_reminded"] = map[string]any{"user_index": latestUser, "open_set": openSet}`.
     - Take up to the first 3 titles in `openTitles`, truncate each to at most 60 runes with `runePrefix(title, 60)`, and join them with `", "`. If more than 3 open titles exist, append `fmt.Sprintf(", +%d more", len(openTitles)-len(shown))`.
     - Set the reminder reason to:
       `"Approved work is still open in your task list: " + strings.Join(shown, ", ") + more + ". Continue it now unless it needs a real user decision; if an item is obsolete, mark it completed or remove it."`
     - Append this reason to `reasons` and set `kinds["todos"] = true`.
