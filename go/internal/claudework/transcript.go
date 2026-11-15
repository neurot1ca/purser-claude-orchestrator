package claudework

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const transcriptLimit = 1 << 20

type transcriptUsage struct {
	Input       *uint64 `json:"input_tokens"`
	Output      *uint64 `json:"output_tokens"`
	CacheRead   *uint64 `json:"cache_read_input_tokens"`
	CacheCreate *uint64 `json:"cache_creation_input_tokens"`
}
type transcriptMessage struct {
	ID              string `json:"id"`
	MessageID       string `json:"message_id"`
	Role, Model     string
	Effort          any              `json:"effort"`
	ReasoningEffort any              `json:"reasoning_effort"`
	Side            bool             `json:"isSidechain"`
	Usage           *transcriptUsage `json:"usage"`
	Nested          *struct {
		Usage *transcriptUsage `json:"usage"`
	} `json:"message"`
	Content []struct{ Type, Text string }
}
type transcriptEvent struct {
	Type, Timestamp, Model, ID, RequestID string
	Effort                                any               `json:"effort"`
	ReasoningEffort                       any               `json:"reasoning_effort"`
	Session                               string            `json:"sessionId"`
	SessionAlt                            string            `json:"session_id"`
	Agent                                 string            `json:"agentId"`
	AgentAlt                              string            `json:"agent_id"`
	Side                                  bool              `json:"isSidechain"`
	SideAlt                               bool              `json:"sidechain"`
	Usage                                 *transcriptUsage  `json:"usage"`
	Message                               transcriptMessage `json:"message"`
}

// ExtractTranscript adds only allowlisted metadata; unavailable input stays partial.
func ExtractTranscript(record *Record) {
	if record == nil {
		return
	}
	deadline := time.Now().Add(50 * time.Millisecond)
	unknown := func(reason string) {
		record.MetadataStatus = "partial"
		record.Usage = &Usage{Source: "transcript", Completeness: "unknown", UnknownReason: reason}
	}
	path, err := validatedTranscriptPath(*record)
	if err != nil || time.Now().After(deadline) {
		unknown("transcript_unavailable")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		unknown("transcript_unavailable")
		return
	}
	defer f.Close()
	if info, e := f.Stat(); e != nil || !info.Mode().IsRegular() {
		unknown("transcript_unavailable")
		return
	}
	if time.Now().After(deadline) {
		unknown("transcript_deadline")
		return
	}
	r := bufio.NewReader(io.LimitReader(f, transcriptLimit+1))
	seen := map[string][4]uint64{}
	var sum [4]uint64
	complete, partial, any := true, false, false
	unknownReason := "transcript_incomplete"
	size := 0
	var start, last time.Time
	for {
		if time.Now().After(deadline) {
			partial = true
			break
		}
		line, e := r.ReadString('\n')
		size += len(line)
		if size > transcriptLimit {
			partial = true
			break
		}
		if strings.TrimSpace(line) != "" && !readTranscriptLine(record, strings.TrimSpace(line), seen, &sum, &complete, &any, &unknownReason, &start, &last) {
			partial = true
		}
		if e != nil {
			if e != io.EOF {
				partial = true
			}
			break
		}
	}
	if !start.IsZero() {
		record.StartedAt = start.UTC().Format(time.RFC3339Nano)
	}
	if !last.IsZero() {
		record.LastResponseAt = last.UTC().Format(time.RFC3339Nano)
	}
	if !partial && complete && any {
		record.Usage = &Usage{InputTokens: counter(sum[0]), OutputTokens: counter(sum[1]), CacheReadInputTokens: counter(sum[2]), CacheCreationInputTokens: counter(sum[3]), Source: "transcript", Completeness: "complete"}
		record.MetadataStatus = "complete"
	} else {
		record.Usage = &Usage{Source: "transcript", Completeness: "unknown", UnknownReason: unknownReason}
		record.MetadataStatus = "partial"
	}
}

func validatedTranscriptPath(r Record) (string, error) {
	if r.TranscriptPath == "" || r.SessionID == nil {
		return "", errors.New("missing transcript identity")
	}
	abs, e := filepath.Abs(r.TranscriptPath)
	if e != nil {
		return "", e
	}
	path, e := filepath.EvalSymlinks(abs)
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(abs))
	if e != nil || parentErr != nil || !samePath(filepath.Join(parent, filepath.Base(abs)), path) {
		return "", errors.New("transcript link rejected")
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if r.Kind == "subagent" {
		if r.AgentID == nil || (!samePath(name, *r.AgentID) && !samePath(name, "agent-"+*r.AgentID)) || !samePath(filepath.Base(filepath.Dir(path)), "subagents") {
			return "", errors.New("subagent path identity mismatch")
		}
	} else if !samePath(name, *r.SessionID) {
		return "", errors.New("session path identity mismatch")
	}
	if runtime.GOOS == "windows" && len(path) > 240 {
		if strings.HasPrefix(path, `\\`) {
			path = `\\?\UNC\` + strings.TrimLeft(path, `\`)
		} else {
			path = `\\?\` + path
		}
	}
	return path, nil
}

func readTranscriptLine(r *Record, line string, seen map[string][4]uint64, sum *[4]uint64, complete, any *bool, unknownReason *string, start, last *time.Time) bool {
	var e transcriptEvent
	if json.Unmarshal([]byte(line), &e) != nil {
		return false
	}
	session := e.Session
	if session == "" {
		session = e.SessionAlt
	}
	if session != "" && session != *r.SessionID {
		return true
	}
	agent := e.Agent
	if agent == "" {
		agent = e.AgentAlt
	}
	if r.Kind == "subagent" && r.AgentID != nil && agent != "" && agent != *r.AgentID {
		return true
	}
	m := e.Message
	side := e.Side || e.SideAlt || m.Side
	if r.Kind == "session" && side || r.Kind == "subagent" && !side {
		return true
	}
	role := m.Role
	if e.Type != "assistant" && role != "assistant" {
		if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil && (start.IsZero() || t.Before(*start)) {
			*start = t
		}
		return true
	}
	if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
		if start.IsZero() || t.Before(*start) {
			*start = t
		}
		if last.IsZero() || t.After(*last) {
			*last = t
		}
	}
	if r.Model == "" {
		r.Model = m.Model
		if r.Model == "" {
			r.Model = e.Model
		}
		if r.Model != "" {
			r.ModelSource = "transcript"
		}
	}
	if r.Effort == "" {
		for _, value := range []interface{}{m.Effort, m.ReasoningEffort, e.Effort, e.ReasoningEffort} {
			if effort := effortString(value); effort != "" {
				r.Effort = effort
				break
			}
		}
	}
	for _, b := range m.Content {
		if b.Type == "text" && b.Text != "" {
			r.FinalExcerpt = safeExcerpt(b.Text)
		}
	}
	u := m.Usage
	if u == nil && m.Nested != nil {
		u = m.Nested.Usage
	}
	if u == nil {
		u = e.Usage
	}
	if u == nil || m.ID == "" && m.MessageID == "" && e.RequestID == "" && e.ID == "" {
		*complete = false
		return true
	}
	id := m.ID
	if id == "" {
		id = m.MessageID
	}
	if id == "" {
		id = e.RequestID
	}
	if id == "" {
		id = e.ID
	}
	values := []*uint64{u.Input, u.Output, u.CacheRead, u.CacheCreate}
	var v [4]uint64
	for i, p := range values {
		if p == nil {
			*complete = false
			return true
		}
		v[i] = *p
	}
	if old, ok := seen[id]; ok {
		if old != v {
			*complete = false
		}
	} else {
		for i := range v {
			if v[i] > ^uint64(0)-sum[i] {
				*complete = false
				*unknownReason = "usage_overflow"
				return true
			}
		}
		seen[id] = v
		for i := range v {
			sum[i] += v[i]
		}
		*any = true
	}
	return true
}

func safeExcerpt(s string) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_{}[]()#+-.!|>", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	s = b.String()
	if len(s) > 240 {
		s = s[:240]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func counter(v uint64) *uint64 { return &v }
