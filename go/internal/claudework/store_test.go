package claudework

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"purser-hook/internal/contextio"
)

func sampleRecord() Record {
	session, agent := "session-1", "agent-1"
	return Record{SchemaVersion: 1, ProjectKey: "project", Kind: "subagent", SessionID: &session, AgentID: &agent, State: "running", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), MetadataStatus: "partial"}
}

func TestUpdateRejectsMalformedAndOversized(t *testing.T) {
	root := t.TempDir()
	bad := sampleRecord()
	bad.State = "SUCCEEDED"
	if err := Update(root, bad); err == nil {
		t.Fatal("accepted unsupported state")
	}
	large := sampleRecord()
	large.Model = strings.Repeat("m", maxRecordBytes)
	if err := Update(root, large); err == nil {
		t.Fatal("accepted oversized encoded record")
	}
	excerpt := sampleRecord()
	excerpt.FinalExcerpt = strings.Repeat("x", 241)
	if err := Update(root, excerpt); err == nil {
		t.Fatal("accepted oversized excerpt")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "claude_work")); len(entries) != 0 {
		t.Fatalf("invalid input created state: %v", entries)
	}
	record := sampleRecord()
	dir := filepath.Join(root, "claude_work")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(dir, ".writer.tmp")
	if err := os.WriteFile(temp, []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Update(root, record); err != nil {
		t.Fatalf("fresh interrupted temp was not reclaimed under the writer lock: %v", err)
	}
}

func TestUpdateRejectsOversizedExistingRecord(t *testing.T) {
	root, record := t.TempDir(), sampleRecord()
	path, err := Path(root, record.ProjectKey, record)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, make([]byte, maxRecordBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Update(root, record); err == nil {
		t.Fatal("accepted oversized existing record")
	}
}

func TestUpdateReclaimsOrphanFromInterruptedWrite(t *testing.T) {
	root, record := t.TempDir(), sampleRecord()
	if err := Update(root, record); err != nil {
		t.Fatal(err)
	}
	path, _ := Path(root, record.ProjectKey, record)
	temp := filepath.Join(filepath.Dir(path), ".writer.tmp")
	lock, err := acquire(filepath.Join(filepath.Dir(path), ".writer.lock"), time.Now().Add(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer lock.Close()
		defer contextio.Unlock(lock)
		// An interrupted writer releases its lock but leaves incomplete JSON.
		if err := os.WriteFile(temp, []byte(`{"schema_version":`), 0600); err != nil {
			t.Fatal(err)
		}
	}()
	record.State, record.FinalExcerpt = "response_finished", "complete response"
	if err := Update(root, record); err != nil {
		t.Fatalf("orphan blocked the next update: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Record
	if err := json.Unmarshal(data, &got); err != nil || got.State != record.State || got.FinalExcerpt != record.FinalExcerpt {
		t.Fatalf("recovered update was incomplete: %s (%v)", data, err)
	}
	if _, err := os.Lstat(temp); !os.IsNotExist(err) {
		t.Fatalf("orphan temp remains after publication: %v", err)
	}
}

func TestUpdateConcurrentAndFailedPublication(t *testing.T) {
	root := t.TempDir()
	const writers = 12
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Update(root, sampleRecord()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	recordPath, _ := Path(root, "project", sampleRecord())
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var got Record
	if err = json.Unmarshal(before, &got); err != nil || got.ID == "" || got.Provider != "claude" {
		t.Fatalf("incomplete record: %s (%v)", before, err)
	}
	oldReplace := replaceClaudeWorkFile
	replaceClaudeWorkFile = func(string, string) error { return errors.New("publication rejected") }
	t.Cleanup(func() { replaceClaudeWorkFile = oldReplace })
	if err = Update(root, sampleRecord()); err == nil {
		t.Fatal("publication failure was hidden")
	}
	after, err := os.ReadFile(recordPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed replacement changed previous record: %v", err)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(recordPath), ".writer.tmp")); !os.IsNotExist(err) {
		t.Fatalf("failed publication left temp: %v", err)
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(recordPath), ".writer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = contextio.TryLock(lock); err != nil {
		t.Fatal(err)
	}
	defer contextio.Unlock(lock)
	start := time.Now()
	if err = Update(root, sampleRecord()); err == nil {
		t.Fatal("contended update succeeded")
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("lock wait exceeded bound: %s", time.Since(start))
	}
}
