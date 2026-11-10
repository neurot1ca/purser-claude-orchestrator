package jobs

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"purser-hook/internal/testevidence"
)

const AgyBackgroundTaskAbandonedCode = "AGY_BACKGROUND_TASK_ABANDONED"

// EventResult is the interpretation of an agent's JSON event stream.
type EventResult struct {
	ThreadID          string           `json:"threadId,omitempty"`
	Status            string           `json:"status"`
	Failure           string           `json:"failure,omitempty"`
	LastStepReason    string           `json:"lastStepReason,omitempty"`
	MissingCompletion bool             `json:"missingCompletion,omitempty"`
	Final             string           `json:"final,omitempty"`
	Notes             []string         `json:"notes,omitempty"`
	Findings          []map[string]any `json:"findings,omitempty"`
	Warnings          []map[string]any `json:"warnings,omitempty"`
	Usage             map[string]any   `json:"usage,omitempty"`
}

// EventStream consumes output as it arrives so ThreadID is available before
// the child exits, including when a later event fails.
type EventStream struct {
	agent          string
	expectedThread string
	pending        []byte
	raw            []byte
	result         EventResult
	text           []string
	fallbackText   []string
	events         []map[string]any
}

func NewEventStream(agent string, expectedThread ...string) *EventStream {
	stream := &EventStream{agent: agent, result: EventResult{Status: "incomplete"}}
	if len(expectedThread) > 0 {
		stream.expectedThread = expectedThread[0]
	}
	return stream
}

// Write accepts arbitrary stdout chunks. A final unterminated JSON line is
// consumed by Result, matching the bridge's EOF behavior.
func (s *EventStream) Write(chunk []byte) string {
	s.raw = append(s.raw, chunk...)
	if len(s.raw) > 4000 {
		s.raw = append(s.raw[:0], s.raw[len(s.raw)-4000:]...)
	}
	s.pending = append(s.pending, chunk...)
	for {
		i := bytes.IndexByte(s.pending, '\n')
		if i < 0 {
			return s.result.ThreadID
		}
		s.consume(s.pending[:i])
		s.pending = append(s.pending[:0], s.pending[i+1:]...)
	}
}

func (s *EventStream) consume(line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var event map[string]any
	if json.Unmarshal(line, &event) != nil || event == nil {
		return
	}
	kind, _ := event["type"].(string)
	s.events = append(s.events, event)
	session := ""
	var failure string
	terminal, success := false, false
	switch s.agent {
	case "agy":
		session = nonemptyString(event["conversation_id"])
		status, _ := event["status"].(string)
		response, responseOK := event["response"].(string)
		success = strings.EqualFold(status, "success") && responseOK
		terminal = status != ""
		if success {
			s.text = append(s.text, response)
		} else {
			if responseOK && response != "" {
				s.fallbackText = append(s.fallbackText, response)
			}
			failure = firstString(event["error"], status, "Antigravity completion missing")
		}
	case "opencode":
		session = nonemptyString(event["sessionID"])
		if kind == "step_finish" {
			part, _ := event["part"].(map[string]any)
			reason := nonemptyString(part["reason"])
			if reason != "stop" {
				s.result.LastStepReason = reason
			} else {
				s.result.LastStepReason = ""
			}
		}
		if kind == "error" {
			failure = eventMessage(event)
		}
		if kind == "tool_use" {
			part, _ := event["part"].(map[string]any)
			state, _ := part["state"].(map[string]any)
			if state["status"] == "error" {
				s.result.Notes = append(s.result.Notes, openCodeToolNote(part, state))
			}
		}
		part, _ := event["part"].(map[string]any)
		if kind == "text" {
			if value, ok := part["text"].(string); ok {
				s.text = append(s.text, value)
			}
		}
		terminal = kind == "step_finish" && part["reason"] == "stop" || failure != ""
		success = kind == "step_finish" && part["reason"] == "stop" && failure == ""
	case "codex":
		if kind == "thread.started" {
			session = nonemptyString(event["thread_id"])
		}
		if kind == "error" || kind == "turn.failed" {
			failure = eventMessage(event)
		}
		item, _ := event["item"].(map[string]any)
		if kind == "item.completed" && item["type"] == "agent_message" {
			if value, ok := item["text"].(string); ok {
				s.text = append(s.text, value)
			}
		}
		terminal = kind == "turn.completed" || kind == "turn.failed" || kind == "error"
		success = kind == "turn.completed" && failure == ""
		if kind == "turn.completed" {
			if usage, ok := event["usage"].(map[string]any); ok {
				s.result.Usage = codexUsage(usage)
			}
		}
	case "claude":
		if kind == "result" {
			session, _ = event["session_id"].(string)
			message, _ := event["result"].(string)
			if message != "" {
				s.text = append(s.text, message)
			}
			terminal = true
			success = event["is_error"] != true && event["subtype"] == "success"
			if !success {
				failure = firstString(event["result"], "Claude completion failed")
			}
		}
	case "cursor":
		session = nonemptyString(event["session_id"])
		if session != "" && s.result.ThreadID != "" && session != s.result.ThreadID {
			failure = "Cursor session identity mismatch"
		}
		if kind == "result" {
			message, messageOK := event["result"].(string)
			valid := event["subtype"] == "success" && event["is_error"] == false && messageOK && session != ""
			if s.expectedThread != "" && session != s.expectedThread {
				failure = "Cursor did not resume the requested session"
				valid = false
			}
			terminal = true
			success = valid && failure == ""
			if success {
				s.text = append(s.text, message)
			} else if failure == "" {
				failure = "Cursor completion is missing valid terminal fields"
			}
		}
	}
	if s.result.ThreadID == "" && session != "" {
		s.result.ThreadID = session
	}
	if failure != "" {
		if s.result.Failure != "" {
			s.result.Failure += "\n" + failure
		} else {
			s.result.Failure = failure
		}
		s.result.Status = "error"
	} else if terminal && success && s.result.Failure == "" {
		s.result.Status = "success"
	}
}

func (s *EventStream) Events() []map[string]any { return s.events }
func (s *EventStream) RawOutput() string        { return string(s.raw) }
func (s *EventStream) FallbackFinal() string    { return strings.Join(s.fallbackText, "") }

func agyBackgroundTaskAbandoned(agent string, exitCode int, stdout, stderr, output string, result EventResult) bool {
	if agent != "agy" || exitCode == 0 {
		return false
	}
	combined := strings.ToLower(stdout + "\n" + stderr + "\n" + output + "\n" + result.Failure + "\n" + result.Final)
	return strings.Contains(combined, "root agent idle") && strings.Contains(combined, "background task")
}

type testEvidenceRun struct {
	index int
	value map[string]any
}

func collectTestEvidence(events []map[string]any) ([]testEvidenceRun, bool) {
	latest := make(map[string]testEvidenceRun)
	outputs := make(map[string]string)
	lastEdit := -1
	for i, event := range events {
		kind, _ := event["type"].(string)
		item, _ := event["item"].(map[string]any)
		if kind == "item.updated" && item["type"] == "command_execution" {
			if id, _ := item["id"].(string); id != "" {
				if delta, _ := event["output_delta"].(string); delta != "" {
					outputs[id] += delta
				} else if output := commandOutput(item); output != "" {
					outputs[id] = output
				}
			}
			continue
		}
		if kind != "item.completed" {
			continue
		}
		if item["type"] == "file_change" {
			lastEdit = i
		}
		if item["type"] != "command_execution" {
			continue
		}
		command, _ := item["command"].(string)
		target, exitCode := testevidence.ExitCode(command, item["exit_code"])
		if target == "" {
			continue
		}
		output := commandOutput(item)
		if output == "" {
			if id, _ := item["id"].(string); id != "" {
				output = outputs[id]
			}
		}
		if output == "" {
			status := firstString(item["status"], "unknown")
			marker := "[no output captured; item status: " + status
			if item["timed_out"] == true || strings.Contains(strings.ToLower(status), "timeout") {
				marker += "; timeout: true"
			}
			output = marker + "]"
		}
		lines := strings.Split(output, "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		tail := strings.Join(lines, "\n")
		if len(tail) > 4096 {
			tail = tail[len(tail)-4096:]
			for !utf8.ValidString(tail) {
				tail = tail[1:]
			}
		}
		latest[target] = testEvidenceRun{index: i, value: map[string]any{
			"command": command, "exit_code": exitCode, "output_tail": tail,
		}}
	}
	runs := make([]testEvidenceRun, 0, len(latest))
	failed := false
	for _, item := range latest {
		if item.index < lastEdit {
			continue
		}
		runs = append(runs, item)
		if code, ok := item.value["exit_code"].(int); ok && code != 0 {
			failed = true
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].index < runs[j].index })
	return runs, failed
}

func commandOutput(item map[string]any) string {
	for _, key := range []string{"aggregated_output", "output", "stdout", "stderr"} {
		if output, _ := item[key].(string); output != "" {
			return output
		}
	}
	return ""
}

// TestEvidence returns the latest completed test-runner command for each
// distinct command string after the last file change, keeping at most six
// runs without dropping an unknown status.
func TestEvidence(events []map[string]any) []map[string]any {
	runs, _ := collectTestEvidence(events)
	out := make([]map[string]any, len(runs))
	for i, item := range runs {
		out[i] = item.value
	}
	return testevidence.Limit(out)
}

// TestEvidenceFailed reports whether any latest-per-command test run failed,
// including runs omitted from TestEvidence's six-entry output cap.
func TestEvidenceFailed(events []map[string]any) bool {
	_, failed := collectTestEvidence(events)
	return failed
}

func testRunnerCommand(command string) bool {
	return testRunnerTarget(command) != ""
}

func testRunnerTarget(command string) string {
	return testevidence.Target(command)
}

// Result flushes an unterminated final line and returns a snapshot.
func (s *EventStream) Result(interrupted bool, exitCode int, stderr string) EventResult {
	if len(bytes.TrimSpace(s.pending)) > 0 {
		s.consume(s.pending)
		s.pending = nil
	}
	if interrupted {
		s.result.Status = "interrupted"
		s.result.Failure = "cancelled"
	}
	if s.agent == "opencode" && s.result.Status == "incomplete" && !interrupted {
		s.result.MissingCompletion = true
	}
	if !interrupted && exitCode != 0 && s.result.Status != "error" {
		s.result.Status = "error"
		s.result.Failure = stderrTail(stderr)
		if s.result.Failure == "" {
			s.result.Failure = "exit code " + strconv.Itoa(exitCode)
		}
	}
	s.result.Final = strings.Join(s.text, "")
	if s.agent == "codex" && len(s.text) > 0 {
		s.result.Final = s.text[len(s.text)-1]
	}
	if s.agent == "cursor" && s.result.Status != "success" {
		s.result.Final = ""
	}
	if interrupted {
		s.result.Final = ""
		s.result.Usage = nil
	}
	return s.result
}

func nonemptyString(value any) string {
	s, _ := value.(string)
	if s == "" {
		return ""
	}
	return s
}

func firstString(value any, fallback string, rest ...string) string {
	if s, ok := value.(string); ok && s != "" {
		return s
	}
	if fallback != "" {
		return fallback
	}
	for _, s := range rest {
		if s != "" {
			return s
		}
	}
	return ""
}

func eventMessage(event map[string]any) string {
	parts := make([]string, 0, 4)
	for _, value := range []any{event["message"], event["error"]} {
		switch item := value.(type) {
		case string:
			parts = append(parts, item)
		case map[string]any:
			for _, key := range []string{"message", "code"} {
				if s, ok := item[key].(string); ok {
					parts = append(parts, s)
				}
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, ": ")
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func openCodeToolNote(part, state map[string]any) string {
	tool := nonemptyString(part["tool"])
	if tool == "" {
		tool = "unknown"
	}
	error := ""
	switch value := state["error"].(type) {
	case string:
		error = value
	case map[string]any:
		error = eventMessage(map[string]any{"error": value})
	}
	if error == "" {
		error = "OpenCode tool failed"
	}
	return "OpenCode tool error: " + tool + ": " + error
}

func codexUsage(value any) map[string]any {
	usage, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	allowed := map[string]bool{"input_tokens": true, "cached_input_tokens": true, "output_tokens": true, "reasoning_output_tokens": true}
	out := make(map[string]any)
	for key, value := range usage {
		if !allowed[key] {
			continue
		}
		n, ok := value.(float64)
		if ok && !math.IsInf(n, 0) && !math.IsNaN(n) && n >= 0 {
			out[key] = n
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stderrTail(stderr string) string {
	runes := []rune(stderr)
	if len(runes) > 4000 {
		runes = runes[len(runes)-4000:]
	}
	return string(runes)
}
