package enforce

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"purser-hook/internal/contextio"
)

var postReceiptLockFile = contextio.LockFile
var postReceiptStateWriter = writePostReceiptState

func postReceiptFailure(root string, err error) {
	event := map[string]any{"hook_event_name": "PostToolUse", "tool_name": "post_receipt"}
	if writePostDiagnostic(root, event, err) != nil {
		_, _ = os.Stderr.Write(append(diagnosticJSON(postDiagnostic(event, err)), '\n'))
	}
}

func postReceiptStatePath(root, repository string) string {
	return filepath.Join(root, "projects", projectKey(&repository), "telemetry", "compaction_advisor.json")
}

func readPostReceiptState(path, session string) map[string]any {
	state := map[string]any{"session_id": session, "receipt_pending": nil, "dispatch_pending": nil,
		"maintenance_pending": nil, "version_pending": nil, "leader_pending": nil, "fallback_pending": nil}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	var saved map[string]any
	if json.Unmarshal(data, &saved) != nil || saved["session_id"] != session {
		return state
	}
	for key, value := range saved {
		state[key] = value
	}
	return state
}

func writePostReceiptState(path string, state map[string]any) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicWrite(path, data)
}

// queuePostCompletionReceipt stores the finalized completion receipt for the session.
func queuePostCompletionReceipt(root string, event map[string]any, repository string, bundle map[string]any) (result map[string]any) {
	session, model := stringValue(event["session_id"]), stringValue(bundle["model"])
	if repository == "" || session == "" || model == "" {
		return nil
	}
	effort := stringValue(bundle["effort"])
	if effort == "" {
		effort = "unknown"
	}
	receipt := map[string]any{"model": model, "effort": effort, "blocked": false}
	if up, err := contextio.Gear(defaultPreStateRoot(root), projectKey(&repository)); err == nil && up {
		receipt["gear"] = "up"
	}
	if quota, ok := bundle["codex_quota"].(map[string]any); ok {
		for _, key := range []string{"5h_used_pct", "5h_resets_at", "7d_used_pct", "7d_resets_at", "snapshot_at"} {
			value := quota[key]
			if strings.HasSuffix(key, "used_pct") {
				switch n := value.(type) {
				case float64:
					receipt[key] = math.Round(n)
				case int:
					receipt[key] = n
				}
			} else if _, ok := value.(string); ok {
				receipt[key] = value
			}
		}
	}
	path := postReceiptStatePath(root, repository)
	unlock, err := postReceiptLockFile(path + ".lock")
	if err != nil {
		postReceiptFailure(root, fmt.Errorf("completion receipt lock: %w", err))
		return nil
	}
	defer func() {
		if err := unlock(); err != nil {
			postReceiptFailure(root, fmt.Errorf("completion receipt lock release: %w", err))
			result = nil
		}
	}()
	state := readPostReceiptState(path, session)
	language := stringValue(state["last_context_language"])
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "purser-handoff.md")); err == nil {
		if recorded := contextio.HandoffUserLanguage(string(data)); recorded != "" {
			language = recorded
			contextio.RecordUserLanguage(root, recorded)
		}
	}
	if language != "" {
		receipt["language"] = language
	}
	state["receipt_pending"] = receipt
	id, err := callKey(event)
	if err == nil {
		if state["receipt_id"] != id {
			state["receipt_delivered"] = false
		}
		state["receipt_id"] = id
	}
	if writeErr := postReceiptStateWriter(path, state); writeErr != nil {
		postReceiptFailure(root, fmt.Errorf("completion receipt state write: %w", writeErr))
		return nil
	}
	if err != nil {
		return nil
	}
	return map[string]any{"id": id, "message": PostReceiptAdvice(receipt, time.Now().UTC()), "repository": repository, "session_id": session}
}

// queuePostReceipt adds a visible dispatch receipt to the next-turn queue.
func queuePostReceipt(root string, event map[string]any, repository, line string) {
	session := stringValue(event["session_id"])
	if repository == "" || session == "" || line == "" {
		return
	}
	path := postReceiptStatePath(root, repository)
	unlock, err := postReceiptLockFile(path + ".lock")
	if err != nil {
		postReceiptFailure(root, fmt.Errorf("dispatch receipt lock: %w", err))
		return
	}
	defer func() {
		if err := unlock(); err != nil {
			postReceiptFailure(root, fmt.Errorf("dispatch receipt lock release: %w", err))
		}
	}()
	state := readPostReceiptState(path, session)
	pending, _ := state["dispatch_pending"].(map[string]any)
	if pending == nil {
		pending = map[string]any{"lines": []any{}, "board": nil, "board_job_ids": []any{}, "blocked": false}
	}
	lines, _ := pending["lines"].([]any)
	for _, existing := range lines {
		if existing == line {
			return
		}
	}
	pending["lines"] = append(lines, line)
	pending["blocked"] = false
	state["dispatch_pending"] = pending
	if err := postReceiptStateWriter(path, state); err != nil {
		postReceiptFailure(root, fmt.Errorf("dispatch receipt state write: %w", err))
	}
}

// markPostGuidanceDelivered records that lifecycle guidance was emitted.
func markPostGuidanceDelivered(root string, event map[string]any, repository string) {
	session := stringValue(event["session_id"])
	if repository == "" || session == "" {
		return
	}
	path := postReceiptStatePath(root, repository)
	unlock, err := postReceiptLockFile(path + ".lock")
	if err != nil {
		postReceiptFailure(root, fmt.Errorf("delivered guidance lock: %w", err))
		return
	}
	defer func() {
		if err := unlock(); err != nil {
			postReceiptFailure(root, fmt.Errorf("delivered guidance lock release: %w", err))
		}
	}()
	state := readPostReceiptState(path, session)
	changed := false
	for _, field := range []string{"maintenance_pending", "version_pending", "leader_pending", "fallback_pending"} {
		if pending, ok := state[field].(map[string]any); ok && pending["delivered"] != true {
			pending["delivered"] = true
			changed = true
		}
	}
	if changed {
		if err := postReceiptStateWriter(path, state); err != nil {
			postReceiptFailure(root, fmt.Errorf("delivered guidance state write: %w", err))
		}
	}
}

// postPendingGuidance renders pending dispatch instructions.
func postPendingGuidance(root string, event map[string]any, repository string) string {
	session := stringValue(event["session_id"])
	if repository == "" || session == "" {
		return ""
	}
	state := readPostReceiptState(postReceiptStatePath(root, repository), session)
	parts := []string{}
	if pending, ok := state["dispatch_pending"].(map[string]any); ok {
		if lines, ok := pending["lines"].([]any); ok && len(lines) > 0 {
			valid, rendered := true, make([]string, 0, len(lines))
			for _, value := range lines {
				line, ok := value.(string)
				if !ok || line == "" {
					valid = false
					break
				}
				rendered = append(rendered, line)
			}
			if valid {
				parts = append(parts, "Write one line in the user's language as `> ⚓ **Codex: ...**`, including every exact dispatch receipt line below verbatim:\n"+strings.Join(rendered, "\n"))
			}
		}
	}
	return strings.Join(parts, "\n")
}

// Claim UI emission durably so cached lifecycle output never repeats a receipt.
func deliverPostCompletionReceipt(response map[string]any, root string) {
	receipt := mapValue(response["completion_receipt"])
	delete(response, "completion_receipt")
	id, message := stringValue(receipt["id"]), stringValue(receipt["message"])
	if !asyncHash.MatchString(id) || message == "" {
		return
	}
	path := filepath.Join(root, "receipt_deliveries", id+".json")
	unlock, err := postReceiptLockFile(path + ".lock")
	if err != nil {
		postReceiptFailure(root, fmt.Errorf("completion delivery lock: %w", err))
		return
	}
	defer func() {
		if err := unlock(); err != nil {
			postReceiptFailure(root, fmt.Errorf("completion delivery lock release: %w", err))
		}
	}()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			postReceiptFailure(root, fmt.Errorf("completion delivery marker read: %w", err))
		}
		return
	}
	if err := atomicAdmissionJSON(path, receipt); err != nil {
		postReceiptFailure(root, fmt.Errorf("completion delivery marker write: %w", err))
		return
	}
	statePath := postReceiptStatePath(root, stringValue(receipt["repository"]))
	release, err := postReceiptLockFile(statePath + ".lock")
	if err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			postReceiptFailure(root, fmt.Errorf("completion delivery marker rollback: %w", removeErr))
		}
		postReceiptFailure(root, fmt.Errorf("completion delivery state lock: %w", err))
		return
	}
	state := readPostReceiptState(statePath, stringValue(receipt["session_id"]))
	if state["receipt_id"] != id {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			postReceiptFailure(root, fmt.Errorf("completion delivery marker rollback: %w", err))
		}
		postReceiptFailure(root, errors.New("completion delivery state does not match receipt"))
		if releaseErr := release(); releaseErr != nil {
			postReceiptFailure(root, fmt.Errorf("completion delivery state lock release: %w", releaseErr))
		}
		return
	}
	state["receipt_delivered"] = true
	if err := postReceiptStateWriter(statePath, state); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			postReceiptFailure(root, fmt.Errorf("completion delivery marker rollback: %w", removeErr))
		}
		postReceiptFailure(root, fmt.Errorf("completion delivery state write: %w", err))
		if releaseErr := release(); releaseErr != nil {
			postReceiptFailure(root, fmt.Errorf("completion delivery state lock release: %w", releaseErr))
		}
		return
	}
	if err := release(); err != nil {
		postReceiptFailure(root, fmt.Errorf("completion delivery state lock release: %w", err))
	}
	if prior := stringValue(response["systemMessage"]); prior != "" {
		message = prior + "\n" + message
	}
	response["systemMessage"] = message
}
