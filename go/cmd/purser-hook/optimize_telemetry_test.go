package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"purser-hook/internal/contextio"
)

func TestOptimizeTelemetryCommand(t *testing.T) {
	state, repo := t.TempDir(), t.TempDir()
	project := contextio.ProjectKey(repo)
	path := filepath.Join(state, "projects", project, "telemetry", "tasks", "task-1.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{
		"project_key": project, "task_id": "task-1",
		"claude_acceptance": map[string]any{"outcome": "accepted", "send_backs": 0},
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PURSER_STATE_DIR", state)
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	output, code := captureOptimizeTelemetry(t, []string{"telemetry"})
	if code != 0 || !strings.Contains(output, "# Purser telemetry") || !strings.Contains(output, "| task-1 | 0 | 0 |") {
		t.Fatalf("unexpected output (code %d):\n%s", code, output)
	}
	output, code = captureOptimizeTelemetry(t, []string{"telemetry", "--json"})
	if code != 0 {
		t.Fatalf("json exit %d", code)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, output)
	}
	if _, code = captureOptimizeTelemetry(t, []string{"telemetry", "bogus"}); code != 2 {
		t.Fatalf("unknown flag exit %d, want 2", code)
	}
}

func captureOptimizeTelemetry(t *testing.T, args []string) (string, int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = writer
	code := optimizeMain(args)
	_ = writer.Close()
	os.Stdout = old
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), code
}
