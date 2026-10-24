package proc

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestSupervisionResweepsLiveGroup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		killErr   error
		confirmed bool
	}{
		{"late_child", nil, true},
		{"denied", syscall.EPERM, false},
		{"exited_before_resweep", syscall.ESRCH, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalKill, originalObserve := killSupervisedGroup, observeSupervisedGroup
			defer func() { killSupervisedGroup, observeSupervisedGroup = originalKill, originalObserve }()
			kills := 0
			killSupervisedGroup = func(pid int, signal syscall.Signal) error {
				if pid != -123 || signal != syscall.SIGKILL {
					t.Fatalf("resweep = %d, %v", pid, signal)
				}
				kills++
				return tc.killErr
			}
			observeSupervisedGroup = func(pid int) (bool, error) {
				if pid != 123 {
					t.Fatalf("group = %d", pid)
				}
				// A child missed the initial kill. A second sweep must stop it;
				// a failed sweep cannot confirm a group still observed alive.
				return kills > 0 && tc.confirmed, nil
			}
			confirmed, err := observeAnchoredGroup(123, time.Now().Add(time.Second))
			if confirmed != tc.confirmed || kills != 1 {
				t.Fatalf("confirmation = %t, %v; resweeps = %d", confirmed, err, kills)
			}
			if tc.confirmed && err != nil || !tc.confirmed && !errors.Is(err, tc.killErr) {
				t.Fatalf("confirmation error = %v", err)
			}
		})
	}
}
