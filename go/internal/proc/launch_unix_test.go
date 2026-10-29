//go:build !windows

package proc

import (
	"os/exec"
	"testing"
)

func TestConfigureProcessTreeKillUnix(t *testing.T) {
	command := exec.Command("unused")
	ConfigureNoWindow(command)
	ConfigureProcessTreeKill(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid || command.Cancel == nil {
		t.Fatalf("process-group configuration = %#v, cancel configured=%t", command.SysProcAttr, command.Cancel != nil)
	}
	if err := command.Cancel(); err != nil {
		t.Fatalf("cancel without process = %v", err)
	}
}
