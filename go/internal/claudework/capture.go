package claudework

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const agentMetaLimit = 8 * 1024

// CaptureAgentMeta reads only the bounded sibling metadata file for this agent.
// The return values report transcript and metadata presence, even when metadata is malformed.
func CaptureAgentMeta(record *Record) (bool, bool) {
	if record == nil || record.Kind != "subagent" || record.AgentID == nil || record.TranscriptPath == "" {
		return false, false
	}
	base := filepath.Base(record.TranscriptPath)
	name := *record.AgentID + ".jsonl"
	metaName := *record.AgentID + ".meta.json"
	if base == "agent-"+name {
		metaName = "agent-" + metaName
	} else if base != name {
		return false, false
	}
	if !strings.EqualFold(filepath.Base(filepath.Dir(record.TranscriptPath)), "subagents") {
		return false, false
	}
	defer captureDispatcherTask(record)
	transcriptExists := false
	if info, err := os.Stat(record.TranscriptPath); err == nil && info.Mode().IsRegular() {
		transcriptExists = true
	}
	f, err := os.Open(filepath.Join(filepath.Dir(record.TranscriptPath), metaName))
	if err != nil {
		return transcriptExists, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return transcriptExists, false
	}
	metaPresent := true
	if info.Size() > agentMetaLimit {
		return transcriptExists, metaPresent
	}
	data, err := io.ReadAll(io.LimitReader(f, agentMetaLimit+1))
	if err != nil || len(data) > agentMetaLimit {
		return transcriptExists, metaPresent
	}
	var meta struct {
		AgentType         string `json:"agentType"`
		PickedEffortModel string `json:"pickedEffortModel"`
		Finished          *bool  `json:"finished"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return transcriptExists, metaPresent
	}
	if record.AgentType == nil && safeText(meta.AgentType, 512) && meta.AgentType != "" {
		record.AgentType = &meta.AgentType
	}
	if record.Model == "" && safeText(meta.PickedEffortModel, 512) && meta.PickedEffortModel != "" {
		record.Model, record.ModelSource = meta.PickedEffortModel, "agent_meta"
	}
	if meta.Finished != nil && *meta.Finished {
		record.State = "finished"
	}
	return transcriptExists, metaPresent
}

// Read only the first user prompt from the identity-checked agent transcript.
// Missing, malformed or ambiguous input must not attribute a later task to it.
func captureDispatcherTask(record *Record) {
	if record.AgentType == nil || *record.AgentType != "purser:dispatcher" || record.TaskLabel != "" {
		return
	}
	path, err := validatedTranscriptPath(*record)
	if err != nil {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return
	}
	decoder := json.NewDecoder(io.LimitReader(f, transcriptLimit))
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		var event struct {
			Type       string
			Session    string `json:"sessionId"`
			SessionAlt string `json:"session_id"`
			Agent      string `json:"agentId"`
			AgentAlt   string `json:"agent_id"`
			Message    struct {
				Role    string
				Content json.RawMessage
			}
		}
		if decoder.Decode(&event) != nil {
			return
		}
		if event.Session != "" && event.Session != *record.SessionID || event.SessionAlt != "" && event.SessionAlt != *record.SessionID ||
			event.Agent != "" && event.Agent != *record.AgentID || event.AgentAlt != "" && event.AgentAlt != *record.AgentID {
			return
		}
		if event.Type != "user" && event.Message.Role != "user" {
			continue
		}
		var prompt string
		if json.Unmarshal(event.Message.Content, &prompt) != nil {
			var blocks []struct{ Type, Text string }
			if json.Unmarshal(event.Message.Content, &blocks) != nil {
				return
			}
			for _, block := range blocks {
				if block.Type == "text" {
					prompt += block.Text + "\n"
				}
			}
		}
		var label, task string
		labels, tasks := 0, 0
		for _, line := range strings.Split(prompt, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "label:") {
				labels++
				label = strings.TrimSpace(strings.TrimPrefix(line, "label:"))
			}
			if strings.HasPrefix(line, "ORCH|task=") {
				tasks++
				task = strings.SplitN(strings.TrimPrefix(line, "ORCH|task="), "|", 2)[0]
			}
		}
		if labels > 1 || tasks > 1 {
			return
		}
		if label == "" {
			label = task
		}
		if !safeText(label, transcriptLimit) {
			return
		}
		runes := []rune(label)
		if len(runes) > 80 {
			runes = runes[:80]
		}
		label = string(runes)
		for len(label) > 240 {
			runes = runes[:len(runes)-1]
			label = string(runes)
		}
		record.TaskLabel = label
		return
	}
}

func effortString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case map[string]any:
		var effort string
		for _, key := range []string{"level", "value"} {
			if candidate, ok := value[key].(string); ok && candidate != "" {
				if effort != "" {
					return ""
				}
				effort = candidate
			}
		}
		return effort
	default:
		return ""
	}
}

// MergeExisting restores previously observed metadata for a terminal update.
func MergeExisting(stateRoot string, record Record) (Record, error) {
	if err := validate(&record); err != nil {
		return record, err
	}
	target, err := Path(stateRoot, record.ProjectKey, record)
	if err != nil {
		return record, err
	}
	if _, err = os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		return record, nil
	} else if err != nil {
		return record, err
	}
	err = mergeExisting(target, &record)
	return record, err
}

// CaptureHookFields copies only the Claude hook fields used by the record.
func CaptureHookFields(record Record, event map[string]any) Record {
	copyString := func(key string, dst *string) {
		if value, ok := event[key].(string); ok && value != "" {
			*dst = value
		}
	}
	if value, ok := event["session_id"].(string); ok && value != "" {
		record.SessionID = &value
	}
	if value, ok := event["agent_id"].(string); ok && value != "" {
		record.AgentID = &value
	}
	if value, ok := event["agent_type"].(string); ok && value != "" {
		record.AgentType = &value
	}
	copyString("transcript_path", &record.TranscriptPath)
	copyString("model", &record.Model)
	for _, key := range []string{"effort", "reasoning_effort"} {
		if value, ok := event[key]; ok {
			if effort := effortString(value); effort != "" {
				record.Effort = effort
				break
			}
			record.Effort = ""
		}
	}
	if record.TranscriptPath != "" {
		record.TranscriptPath = filepath.Clean(record.TranscriptPath)
	}
	return record
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
