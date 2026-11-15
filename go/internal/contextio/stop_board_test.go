package contextio

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoardDisplaySkipsHeldApplyLock(t *testing.T) {
	parent := filepath.Join("..", "..", "..", ".tmp")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(parent, "board-lock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root = resolve(root)
	t.Setenv("PURSER_STATE_DIR", root)
	id := strings.Repeat("a", 64)
	path := filepath.Join(root, "codex_exec_mcp", "jobs", id+".json")
	if err := writeJSONAtomically(path, map[string]any{
		"jobId": id, "state": "SUCCEEDED", "repoRoot": root, "boardDisplayCount": 0,
	}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(root))
	release, err := LockFile(filepath.Join(root, "codex_exec_mcp", "apply", hex.EncodeToString(digest[:])+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	dispatch := map[string]any{"board_job_ids": []any{id}}
	markBoardDisplayed(dispatch, "turn")
	job, ok := readSmallObject(path)
	if !ok || number(job["boardDisplayCount"]) != 0 {
		t.Fatal("display updated despite held apply lock")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	markBoardDisplayed(dispatch, "turn")
	job, ok = readSmallObject(path)
	if !ok || number(job["boardDisplayCount"]) != 1 {
		t.Fatal("skipped display was marked as counted")
	}
}
