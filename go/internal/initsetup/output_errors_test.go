package initsetup

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"purser-hook/internal/profile"
)

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }

func setupForOutputError(t *testing.T, plan bool) (string, string, int) {
	t.Helper()
	root, state, config := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "global.toml")
	t.Setenv("PURSER_STATE_DIR", state)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), ".claude"))
	var stderr strings.Builder
	code := setup(failedWriter{io.ErrClosedPipe}, &stderr, t.TempDir(), root, config, options{
		plan: plan, coder: "codex", reviewer: "codex",
	})
	return root, stderr.String(), code
}

func TestPlanOutputFailure(t *testing.T) {
	_, stderr, code := setupForOutputError(t, true)
	if code != 1 || !strings.Contains(stderr, "writing plan output") {
		t.Fatalf("plan output failure not reported: code=%d stderr=%q", code, stderr)
	}
}

func TestApplyOutputFailure(t *testing.T) {
	root, stderr, code := setupForOutputError(t, false)
	if code != 1 || !strings.Contains(stderr, "writing output after applying changes") {
		t.Fatalf("apply output failure not reported: code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(profile.ProjectRolesPath(profile.StateRoot(), root)); err != nil {
		t.Fatalf("applied change was not retained after output failure: %v", err)
	}
}

func TestBackupCloseFailurePreventsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.toml")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("close failed")
	oldClose := closeBackup
	closeBackup = func(f *os.File) error { _ = f.Close(); return closeErr }
	t.Cleanup(func() { closeBackup = oldClose })
	err := updateLocked(path, func([]byte) ([]byte, error) { return []byte("updated"), nil })
	if !errors.Is(err, closeErr) {
		t.Fatalf("got %v, want backup close error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("original changed after backup close failure: %q, %v", got, err)
	}
}

func TestRoleLockReleaseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.toml")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	releaseErr := errors.New("release failed")
	releases := 0
	oldAcquire := acquireRoleLock
	acquireRoleLock = func(string) (func() error, error) {
		return func() error { releases++; return releaseErr }, nil
	}
	t.Cleanup(func() { acquireRoleLock = oldAcquire })
	err := updateLocked(path, func([]byte) ([]byte, error) { return []byte("updated"), nil })
	if !errors.Is(err, releaseErr) || releases != 1 {
		t.Fatalf("got %v, releases=%d; want release error and attempted release", err, releases)
	}
	editErr := errors.New("edit failed")
	err = updateLocked(path, func([]byte) ([]byte, error) { return nil, editErr })
	if !errors.Is(err, editErr) || errors.Is(err, releaseErr) || releases != 2 {
		t.Fatalf("got %v, releases=%d; want primary edit error and another attempted release", err, releases)
	}
}
