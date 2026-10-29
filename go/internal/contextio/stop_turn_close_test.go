package contextio

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateMaintenanceSlotCloseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.json")
	want := errors.New("close failed")
	prior := maintenanceSlotClose
	maintenanceSlotClose = func(file *os.File) error {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("marker removed before close: %v", err)
		}
		if err := file.Close(); err != nil {
			return err
		}
		return want
	}
	t.Cleanup(func() { maintenanceSlotClose = prior })
	if err := createMaintenanceSlot(path); !errors.Is(err, want) {
		t.Fatalf("createMaintenanceSlot error = %v, want %v", err, want)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker remains after failed close: %v", err)
	}
}

func TestRepairOwnPartialRecordCloseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("{\"event_id\":"), 0600); err != nil {
		t.Fatal(err)
	}
	prior := stopTurnFileClose
	stopTurnFileClose = func(file *os.File) error {
		if err := file.Close(); err != nil {
			return err
		}
		return errors.New("close failed")
	}
	t.Cleanup(func() { stopTurnFileClose = prior })
	if repairOwnPartialRecord(path, []byte(`{"event_id":"id"}`)) {
		t.Fatal("partial-record repair succeeded despite close failure")
	}
}

func TestPersistCodexRetryReportsReleaseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.json")
	if err := os.WriteFile(path, []byte(`{"session_id":"session"}`), 0600); err != nil {
		t.Fatal(err)
	}
	prior := stopRetryAcquireLock
	stopRetryAcquireLock = func(string) (func() error, error) {
		return func() error { return errors.New("release failed") }, nil
	}
	t.Cleanup(func() { stopRetryAcquireLock = prior })
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	got := persistCodexRetry(path, "session", "retry reason")
	_ = w.Close()
	os.Stderr = oldStderr
	diagnostic, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got != "retry reason" {
		t.Fatalf("retry reason = %q", got)
	}
	if !strings.Contains(string(diagnostic), "release failed") {
		t.Fatalf("release failure not reported: %q", diagnostic)
	}
}
