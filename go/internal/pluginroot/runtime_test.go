package pluginroot

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func runtimeScratch(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "..", ".tmp")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestRuntimeExecutable(t *testing.T) {
	root := runtimeScratch(t)
	source := filepath.Join(root, "hook.exe")
	if err := os.WriteFile(source, []byte("executable contents"), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	copy := RuntimeCopy(state, source)
	data, err := os.ReadFile(copy)
	if err != nil || copy == source || string(data) != "executable contents" ||
		filepath.Base(copy) != filepath.Base(source) || filepath.Base(filepath.Dir(filepath.Dir(copy))) != "runtime" {
		t.Fatalf("copy = %q, data = %q, err = %v", copy, data, err)
	}
	info, err := os.Stat(copy)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0755 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if again := RuntimeCopy(state, source); again != copy {
		t.Fatalf("reuse = %q, want %q", again, copy)
	}
	reused, err := os.Stat(copy)
	if err != nil || !os.SameFile(info, reused) || !info.ModTime().Equal(reused.ModTime()) {
		t.Fatalf("copy was replaced on reuse: %v", err)
	}
	// A file in place of the state directory forces a portable copy failure.
	if got := RuntimeCopy(source, source); got != source {
		t.Fatalf("fallback = %q, want %q", got, source)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := RuntimeExecutable(source); got != executable || err != nil {
		t.Fatalf("executable fallback = %q, %v", got, err)
	}
}

func TestPruneRuntime(t *testing.T) {
	root := runtimeScratch(t)
	now := time.Now()
	for name, age := range map[string]time.Duration{"old": 96 * time.Hour, "current": 80 * time.Hour, "newer": 74 * time.Hour, "recent": time.Hour} {
		dir := filepath.Join(root, "runtime", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	root, _ = filepath.Abs(root)
	PruneRuntime(root, filepath.Join(root, "runtime", "current", "hook.exe"), now)
	for _, name := range []string{"old", "current", "newer", "recent"} {
		_, err := os.Stat(filepath.Join(root, "runtime", name))
		if name == "old" && !os.IsNotExist(err) || name != "old" && err != nil {
			t.Fatalf("retention of %s: %v", name, err)
		}
	}
}
