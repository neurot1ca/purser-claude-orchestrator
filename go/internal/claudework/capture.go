package claudework

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
