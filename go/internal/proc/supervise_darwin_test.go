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
		wantErr   error
	}{
		{"late_child", nil, true, nil},
		{"permission_denied_while_stopping", syscall.EPERM, true, nil},
		{"group_gone_before_resweep", syscall.ESRCH, true, nil},
		{"unexpected_error", syscall.EACCES, false, syscall.EACCES},
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
			observations := 0
			observeSupervisedGroup = func(pid int) (bool, error) {
				if pid != 123 {
					t.Fatalf("group = %d", pid)
				}
				observations++
				// The first observation sees a live group. Confirmation after
				// the resweep must come from a later group observation.
				return observations > 1 && tc.confirmed, nil
			}
			confirmed, err := observeAnchoredGroup(123, time.Now().Add(time.Second))
			if confirmed != tc.confirmed || kills != 1 {
				t.Fatalf("confirmation = %t, %v; resweeps = %d", confirmed, err, kills)
			}
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("confirmation error = %v", err)
			}
		})
	}
}
