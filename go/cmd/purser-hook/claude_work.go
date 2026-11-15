package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"purser-hook/internal/claudework"
	"purser-hook/internal/contextio"
	"purser-hook/internal/enforce"
)

func claudeWorkMain() int {
	if len(os.Args) > 2 {
		return 2
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 1
	}
	var event map[string]any
	if json.Unmarshal(data, &event) != nil {
		return 2
	}
	if event["hook_event_name"] == "SessionEnd" {
		defer enqueueCleanup(contextio.StateRoot())
	}
	if err = captureClaudeWork(event); err != nil {
		fmt.Fprintln(os.Stderr, "Claude work observation failed:", err)
		return 1
	}
	return 0
}

func observeClaudeWork(event map[string]any) {
	if err := captureClaudeWork(event); err != nil {
		fmt.Fprintln(os.Stderr, "Claude work observation failed:", err)
	}
}

func captureClaudeWork(event map[string]any) error {
	kind, state := "session", "running"
	switch event["hook_event_name"] {
	case "SubagentStart":
		kind = "subagent"
	case "SubagentStop":
		kind, state = "subagent", "response_finished"
	case "Stop":
		state = "response_finished"
	case "SessionEnd":
		state = "session_ended"
	case "SessionStart":
	default:
		return nil
	}
	terminal := event["hook_event_name"] == "Stop" || event["hook_event_name"] == "SubagentStop" || event["hook_event_name"] == "SessionEnd"
	repository := contextio.ProjectRoot(event)
	if repository == "" {
		return nil
	}
	record := claudework.Record{ProjectKey: contextio.ProjectKey(repository), Kind: kind, State: state, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), MetadataStatus: "partial"}
	// SubagentStop's transcript_path is the parent session, so prefer the agent's own transcript.
	if event["hook_event_name"] == "SubagentStop" {
		if path, ok := event["agent_transcript_path"].(string); ok && path != "" {
			event["transcript_path"] = path
		}
	}
	record = claudework.CaptureHookFields(record, event)
	if kind == "subagent" {
		transcriptPresent, metaPresent := claudework.CaptureAgentMeta(&record)
		if !transcriptPresent && !metaPresent && record.AgentType == nil {
			return nil
		}
	}
	if state == "running" {
		record.StartedAt = record.ObservedAt
	}
	if terminal {
		merged, err := claudework.MergeExisting(contextio.StateRoot(), record)
		if err != nil {
			return err
		}
		record = merged
		startedAt := record.StartedAt
		claudework.ExtractTranscript(&record)
		if startedAt != "" {
			record.StartedAt = startedAt
		}
		record.EndedAt = record.ObservedAt
		if record.StartedAt != "" {
			if start, err := time.Parse(time.RFC3339Nano, record.StartedAt); err == nil {
				duration := time.Since(start).Milliseconds()
				if duration >= 0 {
					record.DurationMS = &duration
				}
			}
		}
	}
	if reason, ok := event["reason"].(string); ok {
		record.EndReason = reason
	}
	if err := claudework.Update(contextio.StateRoot(), record); err != nil {
		return err
	}
	if terminal {
		usage := map[string]any{}
		if record.Usage != nil {
			for name, value := range map[string]*uint64{"input_tokens": record.Usage.InputTokens, "output_tokens": record.Usage.OutputTokens, "cache_read_input_tokens": record.Usage.CacheReadInputTokens, "cache_creation_input_tokens": record.Usage.CacheCreationInputTokens} {
				if value != nil {
					usage[name] = *value
				}
			}
		}
		agentType := ""
		if record.AgentType != nil {
			agentType = *record.AgentType
		}
		sessionID := ""
		if record.SessionID != nil {
			sessionID = *record.SessionID
		}
		telemetryKind := "fresh_session"
		if record.Kind == "subagent" {
			telemetryKind = "subagent"
		}
		identity := claudework.Identity(record.Kind, record.SessionID, record.AgentID)
		return enforce.EmitClaudeWorkUsage(contextio.StateRoot(), repository, sessionID, identity, telemetryKind, agentType, record.Model, state, record.FinalExcerpt, usage, record.DurationMS)
	}
	return nil
}
