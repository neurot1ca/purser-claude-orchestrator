package telemetryhealth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func readHealth(t *testing.T, root string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "telemetry", fileName))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestUpdateAttributesFailuresAndPreservesHistoricalCount(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "telemetry", fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"status":"OK","failure_count":9}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Update(root, "retention", "RETENTION_UNKNOWN_OWNERSHIP", "state-root-entry", true); err != nil {
		t.Fatal(err)
	}
	got := readHealth(t, root)
	if got["failure_count"] != float64(9) || got["lifetime_failure_count"] != float64(1) || got["consecutive_failure_count"] != float64(1) {
		t.Fatalf("legacy/new counts = %#v", got)
	}
	component := got["components"].(map[string]any)["retention"].(map[string]any)
	if component["last_error_code"] != "RETENTION_UNKNOWN_OWNERSHIP" || component["path_class"] != "state-root-entry" || component["status"] != "DEGRADED" {
		t.Fatalf("unattributed retention failure: %#v", component)
	}
}

func TestConcurrentUpdatesPreserveBothComponents(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for _, component := range []string{"retention", "projection"} {
		wg.Add(1)
		go func(component string) {
			defer wg.Done()
			if err := Update(root, component, "CHECK_FAILED", "telemetry", true); err != nil {
				t.Errorf("update %s: %v", component, err)
			}
		}(component)
	}
	wg.Wait()
	components := readHealth(t, root)["components"].(map[string]any)
	if len(components) != 2 {
		t.Fatalf("component updates lost: %#v", components)
	}
}

func TestSuccessfulCheckClearsOnlyItsComponent(t *testing.T) {
	root := t.TempDir()
	if err := Update(root, "retention", "LIMIT_BREACH", "state-root", true); err != nil {
		t.Fatal(err)
	}
	if err := Update(root, "projection", "WRITE_FAILED", "telemetry", true); err != nil {
		t.Fatal(err)
	}
	if err := Update(root, "retention", "", "state-root", false); err != nil {
		t.Fatal(err)
	}
	health := readHealth(t, root)
	components := health["components"].(map[string]any)
	retention := components["retention"].(map[string]any)
	projection := components["projection"].(map[string]any)
	if retention["status"] != "OK" || retention["consecutive_failures"] != float64(0) || retention["lifetime_failures"] != float64(1) {
		t.Fatalf("retention recovery lost history: %#v", retention)
	}
	if projection["status"] != "DEGRADED" || health["status"] != "DEGRADED" || health["consecutive_failure_count"] != float64(1) {
		t.Fatalf("recovery cleared unrelated failure: %#v", health)
	}
}

func TestFailedPublicationPreservesPriorFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "telemetry", fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	prior := []byte(`{"schema_version":1,"status":"OK","failure_count":3}`)
	if err := os.WriteFile(path, prior, 0600); err != nil {
		t.Fatal(err)
	}
	original := renameHealth
	renameHealth = func(string, string) error { return errors.New("publication denied") }
	t.Cleanup(func() { renameHealth = original })
	if err := Update(root, "retention", "CHECK_FAILED", "state-root", true); err == nil {
		t.Fatal("expected publication failure")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(prior) {
		t.Fatalf("prior health file changed: %q, %v", got, err)
	}
}
