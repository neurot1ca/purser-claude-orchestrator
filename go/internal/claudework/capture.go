package claudework

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

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
