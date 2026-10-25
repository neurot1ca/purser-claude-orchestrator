package retention

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRetentionWindowsLockedThreadRecordsDeletionFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "threads", "held.json")
	putFile(t, path, []byte(`{}`), 1000)
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	PruneState(root, 1000+91*86400, false)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("locked thread file was lost: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "telemetry", "telemetry_health.json"))
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]any
	if err := json.Unmarshal(data, &health); err != nil {
		t.Fatal(err)
	}
	component := health["components"].(map[string]any)["retention"].(map[string]any)
	if component["status"] != "DEGRADED" || component["last_error_code"] != "RETENTION_DELETION_FAILED" || component["path_class"] != "state-root-entry-threads" {
		t.Fatalf("locked deletion was not reported: %#v", component)
	}
}
