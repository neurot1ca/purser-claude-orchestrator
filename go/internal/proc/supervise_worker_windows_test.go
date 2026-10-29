//go:build windows

package proc

import (
	"os/exec"
	"testing"
	"time"
)

func TestConfigureWorkerLaunch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prepare    func(*exec.Cmd)
		launch     Launch
		wantFlags  uint32
		wantHidden bool
	}{
		{"zero launch", nil, Launch{}, 0, false},
		{"flags and hide", nil, Launch{CreationFlags: 0x200, HideWindow: true}, 0x200, true},
		{"preserves existing", func(c *exec.Cmd) { Hide(c) }, Launch{CreationFlags: 0x4000}, 0x08004000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command("unused")
			if tc.prepare != nil {
				tc.prepare(command)
			}
			configureWorkerLaunch(command, tc.launch)
			if command.SysProcAttr == nil || command.SysProcAttr.CreationFlags != tc.wantFlags || command.SysProcAttr.HideWindow != tc.wantHidden {
				t.Fatalf("attributes = %#v, want flags %#x hidden %t", command.SysProcAttr, tc.wantFlags, tc.wantHidden)
			}
		})
	}
}

func TestProcessJobEmptyLifecycle(t *testing.T) {
	job, err := NewProcessJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if confirmed, err := job.ConfirmStopped(time.Second); err != nil || !confirmed {
		t.Fatalf("empty job confirmed=%t err=%v", confirmed, err)
	}
	if job.ConfirmDescendantsStopped(false) {
		t.Fatal("confirmed stopped before root exit")
	}
	if !job.ConfirmDescendantsStopped(true) {
		t.Fatal("empty job not confirmed after root exit")
	}
	if err := job.Terminate(1); err != nil {
		t.Fatalf("terminate empty job: %v", err)
	}
	if err := job.Assign(-1); err == nil {
		t.Fatal("assigning an invalid pid succeeded")
	}
}
