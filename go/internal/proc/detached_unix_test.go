//go:build !windows

package proc

import (
	"os/exec"
	"testing"
)

func TestConfigureDetachedUnix(t *testing.T) {
	command := exec.Command("unused")
	configureDetached(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatalf("detached attributes = %#v", command.SysProcAttr)
	}
}
