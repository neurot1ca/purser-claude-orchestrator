package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDistributionRuntimeChecks(t *testing.T) {
	bin := fixtureExecutable(t)
	t.Setenv("FIXTURE_FAIL", "")
	t.Setenv("FIXTURE_BAD", "")
	if err := verifyMCP(bin); err != nil {
		t.Fatalf("valid MCP response: %v", err)
	}
	t.Setenv("FIXTURE_BAD", "init")
	if err := verifyMCP(bin); err == nil || !strings.Contains(err.Error(), "initialize response") {
		t.Fatalf("invalid MCP response accepted: %v", err)
	}
	t.Setenv("FIXTURE_BAD", "ping")
	if err := verifyMCP(bin); err == nil || !strings.Contains(err.Error(), "ping response") {
		t.Fatalf("missing ping response accepted: %v", err)
	}
	t.Setenv("FIXTURE_BAD", "")
	t.Setenv("FIXTURE_FAIL", "1")
	if err := verifyMCP(bin); err == nil || !strings.Contains(err.Error(), "MCP process exited") {
		t.Fatalf("failed MCP process accepted: %v", err)
	}
	t.Setenv("FIXTURE_FAIL", "")
	t.Setenv("FIXTURE_BAD", "")
	root := t.TempDir()
	if err := verifyEvents(bin, root); err != nil {
		t.Fatalf("events rejected: %v", err)
	}
	t.Setenv("FIXTURE_FAIL", "1")
	if err := verifyEvents(bin, t.TempDir()); err == nil || !strings.Contains(err.Error(), "hook event exited") {
		t.Fatalf("failed event accepted: %v", err)
	}
}

func TestVerifyMissingBinaryFixture(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "purser-launcher.js"), []byte("console.error('Go binary missing'); process.exit(1);"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyMissingBinary(root, "fixture"); err != nil {
		t.Fatalf("missing binary was not detected: %v", err)
	}
	if err := verifyMissingBinary(t.TempDir(), "fixture"); err == nil {
		t.Fatal("missing launcher accepted")
	}
}
