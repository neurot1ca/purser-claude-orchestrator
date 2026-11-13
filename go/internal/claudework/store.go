package claudework

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"purser-hook/internal/contextio"
	"purser-hook/internal/retention"
)

const maxRecordBytes = 8 * 1024

var projectKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var replaceClaudeWorkFile = os.Rename

type Usage struct {
	InputTokens              *uint64 `json:"input_tokens"`
	OutputTokens             *uint64 `json:"output_tokens"`
	CacheReadInputTokens     *uint64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *uint64 `json:"cache_creation_input_tokens"`
	Source                   string  `json:"source"`
	Completeness             string  `json:"completeness"`
	UnknownReason            string  `json:"unknown_reason,omitempty"`
}

type Record struct {
	SchemaVersion   int     `json:"schema_version"`
	ID              string  `json:"id"`
	ProjectKey      string  `json:"project_key"`
	Provider        string  `json:"provider"`
	Kind            string  `json:"kind"`
	SessionID       *string `json:"session_id"`
	AgentID         *string `json:"agent_id"`
	AgentType       *string `json:"agent_type"`
	ParentSessionID *string `json:"parent_session_id"`
	ParentSource    string  `json:"parent_source,omitempty"`
	Model           string  `json:"model,omitempty"`
	ModelSource     string  `json:"model_source,omitempty"`
	Effort          string  `json:"effort,omitempty"`
	EffortSource    string  `json:"effort_source,omitempty"`
	StartedAt       string  `json:"started_at,omitempty"`
	LastResponseAt  string  `json:"last_response_at,omitempty"`
	EndedAt         string  `json:"ended_at,omitempty"`
	DurationMS      *int64  `json:"duration_ms"`
	State           string  `json:"state"`
	EndReason       string  `json:"end_reason,omitempty"`
	Usage           *Usage  `json:"usage"`
	FinalExcerpt    string  `json:"final_excerpt,omitempty"`
	TranscriptPath  string  `json:"transcript_path,omitempty"`
	ObservedAt      string  `json:"observed_at"`
	MetadataStatus  string  `json:"metadata_status"`
}

func Identity(kind string, sessionID, agentID *string) string {
	var session, agent string
	if sessionID != nil {
		session = *sessionID
	}
	if agentID != nil {
		agent = *agentID
	}
	h := sha256.Sum256([]byte(kind + "\x00" + session + "\x00" + agent))
	return hex.EncodeToString(h[:])
}

func Path(stateRoot, projectKey string, record Record) (string, error) {
	if stateRoot == "" || !projectKeyPattern.MatchString(projectKey) || !oneOf(record.Kind, "subagent", "session") || !identityPartSafe(record.SessionID) || !identityPartSafe(record.AgentID) {
		return "", errors.New("unsafe Claude work identity")
	}
	if runtime.GOOS == "windows" {
		projectKey = strings.ToLower(projectKey)
	}
	return filepath.Join(stateRoot, "claude_work", projectKey+"-"+Identity(record.Kind, record.SessionID, record.AgentID)+".json"), nil
}

// Update validates, merges and atomically publishes one observer record.
func Update(stateRoot string, record Record) error {
	deadline := time.Now().Add(100 * time.Millisecond)
	if runtime.GOOS == "windows" {
		record.ProjectKey = strings.ToLower(record.ProjectKey)
	}
	if err := validate(&record); err != nil {
		return err
	}
	target, err := Path(stateRoot, record.ProjectKey, record)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) > maxRecordBytes {
		return errors.New("Claude work record exceeds 8 KiB or cannot be encoded")
	}
	dir := filepath.Dir(target)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if info, e := os.Lstat(dir); e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe Claude work directory")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	lockPath, tempPath := filepath.Join(dir, ".writer.lock"), filepath.Join(dir, ".writer.tmp")
	if info, e := os.Lstat(lockPath); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe Claude work lock")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	lock, err := acquire(lockPath, deadline)
	if err != nil {
		return err
	}
	defer func() { _ = contextio.Unlock(lock); _ = lock.Close() }()
	if err = os.Chmod(lockPath, 0600); err != nil {
		return err
	}
	if info, e := os.Lstat(tempPath); e == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe Claude work temporary file")
		}
		if e = retention.RetryClaudeWork(deadline, func() error { return os.Remove(tempPath) }); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if err = mergeExisting(target, &record); err != nil {
		return err
	}
	encoded, err = json.Marshal(record)
	if err != nil || len(encoded) > maxRecordBytes {
		return errors.New("merged Claude work record exceeds 8 KiB")
	}
	if !retention.PruneClaudeWork(dir, time.Now(), int64(len(encoded)), target, deadline) {
		return errors.New("Claude work store is at capacity")
	}
	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(encoded); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = retention.RetryClaudeWork(deadline, func() error { return os.Remove(tempPath) })
		return err
	}
	if err = retention.RetryClaudeWork(deadline, func() error { return replaceClaudeWorkFile(tempPath, target) }); err != nil {
		_ = retention.RetryClaudeWork(deadline, func() error { return os.Remove(tempPath) })
		return err
	}
	return nil
}

func validate(r *Record) error {
	if r.SchemaVersion == 0 {
		r.SchemaVersion = 1
	}
	if r.SchemaVersion != 1 || r.ProjectKey == "" || r.Kind == "" || (r.Kind != "subagent" && r.Kind != "session") || !oneOf(r.State, "running", "response_finished", "session_ended", "unknown") || !oneOf(r.MetadataStatus, "complete", "partial", "degraded") {
		return errors.New("invalid Claude work record")
	}
	for _, p := range []*string{r.SessionID, r.AgentID, r.AgentType, r.ParentSessionID} {
		if p != nil && !safeText(*p, 512) {
			return errors.New("unsafe Claude work metadata")
		}
	}
	if !safeText(r.FinalExcerpt, 240) || strings.ContainsAny(r.FinalExcerpt, "\r\n\u2028\u2029") {
		return errors.New("unsafe Claude work excerpt")
	}
	for _, value := range []string{r.ParentSource, r.Model, r.ModelSource, r.Effort, r.EffortSource, r.StartedAt, r.LastResponseAt, r.EndedAt, r.EndReason, r.ObservedAt, r.MetadataStatus} {
		if !safeText(value, 512) {
			return errors.New("unsafe Claude work metadata")
		}
	}
	if r.ObservedAt == "" {
		return errors.New("missing Claude observation time")
	}
	for _, stamp := range []string{r.StartedAt, r.LastResponseAt, r.EndedAt, r.ObservedAt} {
		if stamp == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		_, offset := parsed.Zone()
		if err != nil || offset != 0 {
			return errors.New("Claude observation times must be UTC")
		}
	}
	if !safeText(r.TranscriptPath, 4096) {
		return errors.New("unsafe Claude transcript path")
	}
	if r.Usage != nil && (!safeText(r.Usage.Source, 128) || !safeText(r.Usage.Completeness, 128) || !safeText(r.Usage.UnknownReason, 256)) {
		return errors.New("unsafe Claude usage metadata")
	}
	if r.DurationMS != nil && *r.DurationMS < 0 {
		return errors.New("invalid Claude duration")
	}
	r.Provider, r.ID = "claude", Identity(r.Kind, r.SessionID, r.AgentID)
	return nil
}

func identityPartSafe(value *string) bool { return value == nil || safeText(*value, 512) }

func safeText(value string, max int) bool {
	return len(value) <= max && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && !strings.ContainsAny(value, "\r\n") && !strings.ContainsFunc(value, unicode.IsControl)
}

func mergeExisting(path string, next *Record) error {
	// Reject links before reading so records cannot redirect observation reads.
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("existing Claude work path is unsafe")
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	var old Record
	if len(data) > maxRecordBytes || json.Unmarshal(data, &old) != nil || old.Provider != "claude" || old.ID != next.ID || old.ID != Identity(old.Kind, old.SessionID, old.AgentID) || old.ProjectKey != next.ProjectKey {
		return errors.New("existing Claude work record is malformed or mismatched")
	}
	if validate(&old) != nil || old.ID != next.ID || old.ProjectKey != next.ProjectKey {
		return errors.New("existing Claude work record is malformed or mismatched")
	}
	if old.StartedAt != "" {
		next.StartedAt = old.StartedAt
	}
	if next.AgentType == nil {
		next.AgentType = old.AgentType
	}
	if next.ParentSessionID == nil {
		next.ParentSessionID, next.ParentSource = old.ParentSessionID, old.ParentSource
	}
	if next.Model == "" {
		next.Model, next.ModelSource = old.Model, old.ModelSource
	}
	if next.Effort == "" {
		next.Effort, next.EffortSource = old.Effort, old.EffortSource
	}
	if next.FinalExcerpt == "" {
		next.FinalExcerpt = old.FinalExcerpt
	}
	if next.TranscriptPath == "" {
		next.TranscriptPath = old.TranscriptPath
	}
	if next.Usage == nil {
		next.Usage = old.Usage
	}
	return nil
}

func acquire(path string, deadline time.Time) (*os.File, error) {
	var f *os.File
	err := retention.RetryClaudeWork(deadline, func() error {
		var openErr error
		f, openErr = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		return openErr
	})
	if err != nil {
		return nil, err
	}
	for {
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, errors.New("Claude work lock deadline exceeded")
		}
		if err = contextio.TryLock(f); err == nil {
			return f, nil
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("Claude work lock timeout: %w", err)
		}
		time.Sleep(min(time.Millisecond, time.Until(deadline)))
	}
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
