package claudework

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reconcile recovers missing SubagentStop observations at existing lifecycle hooks.
// All reads are bounded; a locked or malformed observer file leaves other records usable.
func Reconcile(stateRoot, projectKey string, now time.Time) {
	dir := filepath.Join(stateRoot, "claude_work")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var running []Record
	ended := map[string]string{}
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), projectKey+"-") || !strings.HasSuffix(entry.Name(), ".json") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		count++
		if count > 2048 {
			break
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxRecordBytes {
			continue
		}
		f, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
		f.Close()
		var record Record
		if err != nil || len(data) > maxRecordBytes || json.Unmarshal(data, &record) != nil || record.ProjectKey != projectKey || record.Provider != "claude" ||
			record.ID != Identity(record.Kind, record.SessionID, record.AgentID) || entry.Name() != projectKey+"-"+record.ID+".json" || validate(&record) != nil || record.SessionID == nil {
			continue
		}
		if record.Kind == "session" && record.State == "session_ended" {
			ended[*record.SessionID] = record.EndedAt
			if record.EndedAt == "" {
				ended[*record.SessionID] = record.ObservedAt
			}
		} else if record.Kind == "subagent" && record.State == "running" {
			running = append(running, record)
		}
	}
	for _, record := range running {
		reconcileRunning(&record, ended[*record.SessionID], now)
		if record.State == "running" {
			continue
		}
		if start, err := time.Parse(time.RFC3339Nano, record.StartedAt); err == nil {
			end, _ := time.Parse(time.RFC3339Nano, record.EndedAt)
			if elapsed := end.Sub(start).Milliseconds(); elapsed >= 0 {
				record.DurationMS = &elapsed
			}
		}
		// Update retains the store's locking, atomic publication and Windows retry policy.
		_ = Update(stateRoot, record)
	}
}

func reconcileRunning(record *Record, sessionEnded string, now time.Time) {
	defer func() {
		if record.State == "running" && sessionEnded != "" {
			record.State, record.EndedAt = "session_ended", sessionEnded
		}
	}()
	if record.AgentID == nil || record.TranscriptPath == "" {
		return
	}
	session, agent := *record.SessionID, *record.AgentID
	for _, id := range []string{session, agent} {
		if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\:`) {
			return
		}
	}
	path := filepath.Clean(record.TranscriptPath)
	if !filepath.IsAbs(path) {
		return
	}
	if samePath(filepath.Base(path), session+".jsonl") {
		path = filepath.Join(filepath.Dir(path), session, "subagents", "agent-"+agent+".jsonl")
	}
	if !samePath(filepath.Base(path), "agent-"+agent+".jsonl") || !samePath(filepath.Base(filepath.Dir(path)), "subagents") ||
		!samePath(filepath.Base(filepath.Dir(filepath.Dir(path))), session) {
		return
	}
	agentRecord := *record
	agentRecord.TranscriptPath = path
	var transcriptInfo os.FileInfo
	if validated, err := validatedTranscriptPath(agentRecord); err == nil {
		if f, err := os.Open(validated); err == nil {
			transcriptInfo, err = f.Stat()
			if err == nil && transcriptInfo.Mode().IsRegular() && transcriptInterrupted(f, transcriptInfo.Size(), session, agent) {
				record.State, record.EndedAt = "interrupted", transcriptInfo.ModTime().UTC().Format(time.RFC3339Nano)
			}
			f.Close()
		}
	}
	if record.State != "running" {
		return
	}
	// Metadata and parent closure are independent of transcript read success or size.
	metaPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".meta.json"
	if info, err := os.Lstat(metaPath); err == nil && info.Mode().IsRegular() && info.Size() <= agentMetaLimit {
		if f, err := os.Open(metaPath); err == nil {
			data, err := io.ReadAll(io.LimitReader(f, agentMetaLimit+1))
			f.Close()
			var meta struct {
				Finished bool `json:"finished"`
			}
			if err == nil && len(data) <= agentMetaLimit && json.Unmarshal(data, &meta) == nil && meta.Finished {
				record.State, record.EndedAt = "finished", info.ModTime().UTC().Format(time.RFC3339Nano)
				return
			}
		}
	}
	// Dispatchers may wait on long Codex jobs: idle alone is never terminal.
	if sessionEnded != "" {
		return
	}
	parentPath := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(path))), session+".jsonl")
	if _, err := os.Stat(parentPath); errors.Is(err, os.ErrNotExist) && transcriptInfo != nil && transcriptInfo.Mode().IsRegular() && now.Sub(transcriptInfo.ModTime()) >= 30*time.Minute {
		record.State, record.EndedAt = "session_ended", transcriptInfo.ModTime().UTC().Format(time.RFC3339Nano)
	}
}

func transcriptInterrupted(f *os.File, size int64, session, agent string) bool {
	const tailLimit = 16 * 1024
	offset := max(int64(0), size-tailLimit)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	tail, err := io.ReadAll(io.LimitReader(f, tailLimit))
	if err != nil {
		return false
	}
	if offset > 0 {
		first := bytes.IndexByte(tail, '\n')
		if first < 0 {
			return false
		}
		tail = tail[first+1:]
	}
	tail = bytes.TrimSpace(tail)
	if last := bytes.LastIndexByte(tail, '\n'); last >= 0 {
		tail = tail[last+1:]
	}
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
	if json.Unmarshal(tail, &event) != nil || event.Type != "user" || (event.Message.Role != "" && event.Message.Role != "user") ||
		(event.Session != "" && event.Session != session) || (event.SessionAlt != "" && event.SessionAlt != session) ||
		(event.Agent != "" && event.Agent != agent) || (event.AgentAlt != "" && event.AgentAlt != agent) {
		return false
	}
	var text string
	if json.Unmarshal(event.Message.Content, &text) != nil {
		var blocks []struct{ Type, Text string }
		if json.Unmarshal(event.Message.Content, &blocks) != nil {
			return false
		}
		for _, block := range blocks {
			if block.Type == "text" {
				text += block.Text
			}
		}
	}
	return text == "[Request interrupted by user for tool use]" || text == "[Request interrupted by user]"
}
