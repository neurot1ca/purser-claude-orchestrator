package proc

import (
	"encoding/json"
	"os"
	"testing"
)

func captureWorkerEvent(t *testing.T, launch Launch) supervisionEvent {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runWorker(launch)
	os.Stdout = old
	_ = w.Close()
	defer r.Close()
	var event supervisionEvent
	if err := json.NewDecoder(r).Decode(&event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestStreamFilesSkipsUnsetHandles(t *testing.T) {
	files := streamFiles([3]uintptr{})
	for i, f := range files {
		if f != nil {
			t.Fatalf("stream %d = %v, want nil", i, f)
		}
	}
}

func TestRunWorkerReportsStartFailure(t *testing.T) {
	event := captureWorkerEvent(t, Launch{Args: []string{"purser-no-such-binary-for-test"}, Dir: t.TempDir()})
	if event.Kind != "root" || event.Exit != -1 || event.PID != 0 || event.Error == "" {
		t.Fatalf("event = %+v", event)
	}
}

func TestRunWorkerReportsRootExitCode(t *testing.T) {
	// An unknown test flag makes the test binary exit with status 2 without running tests.
	event := captureWorkerEvent(t, Launch{
		Args: []string{os.Args[0], "-test.purser-unknown-flag"},
		Dir:  t.TempDir(),
	})
	if event.Kind != "root" || event.Exit != 2 || event.PID <= 0 || event.Error != "" {
		t.Fatalf("event = %+v", event)
	}
}
