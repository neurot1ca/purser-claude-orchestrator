//go:build windows

package proc

import (
	"os/exec"
	"testing"
)

func TestConfigureBelowNormalPriorityPreservesHiddenConsole(t *testing.T) {
	command := exec.Command("git")
	ConfigureProcessTreeKill(command)
	ConfigureBelowNormalPriority(command)

	if command.SysProcAttr.CreationFlags != 0x08004200 || !command.SysProcAttr.HideWindow {
		t.Fatalf("process attributes = %#v, want CREATE_NO_WINDOW, priority/process-tree flags, and hidden window", command.SysProcAttr)
	}
}

func TestConfigureBelowNormalPriorityAlone(t *testing.T) {
	command := exec.Command("git")
	ConfigureBelowNormalPriority(command)

	if command.SysProcAttr == nil || command.SysProcAttr.CreationFlags != 0x00004000 {
		t.Fatalf("process attributes = %#v, want flags 0x00004000", command.SysProcAttr)
	}
}

func TestWindowAndTreeKillConfiguration(t *testing.T) {
	command := exec.Command("unused")
	ConfigureNoWindow(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow || command.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatalf("no-window attributes = %#v", command.SysProcAttr)
	}
	ConfigureProcessTreeKill(command)
	if command.SysProcAttr.CreationFlags&0x200 == 0 || command.Cancel == nil {
		t.Fatalf("tree-kill attributes = %#v, cancel configured=%t", command.SysProcAttr, command.Cancel != nil)
	}
	if err := command.Cancel(); err != nil {
		t.Fatalf("cancel without process = %v", err)
	}
	configureDetached(command)
	if !command.SysProcAttr.HideWindow {
		t.Fatal("detached command is visible")
	}
}
