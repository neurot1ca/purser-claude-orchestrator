package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func BuildFakeBinary(t *testing.T, source, output string) {
	t.Helper()
	goExecutable := ""
	if root := runtime.GOROOT(); root != "" {
		candidate := filepath.Join(root, "bin", "go")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err == nil {
			goExecutable = candidate
		}
	}
	if goExecutable == "" {
		var err error
		goExecutable, err = exec.LookPath("go")
		if err != nil {
			t.Fatalf("find Go executable: %v", err)
		}
	}
	command := Hide(exec.Command(goExecutable, "build", "-o", output, source))
	command.Dir = filepath.Dir(source)
	command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake binary: %v: %s", err, result)
	}
}
