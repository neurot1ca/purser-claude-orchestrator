package fsretry

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func withRename(t *testing.T, fn func(string, string) error) {
	t.Helper()
	prior := rename
	priorNow, priorSleep := now, sleep
	rename = fn
	clock := time.Unix(0, 0)
	now = func() time.Time { return clock }
	sleep = func(delay time.Duration) { clock = clock.Add(delay) }
	t.Cleanup(func() { rename, now, sleep = prior, priorNow, priorSleep })
}

func TestReplaceRetriesThenSucceeds(t *testing.T) {
	attempts := 0
	withRename(t, func(string, string) error {
		attempts++
		if attempts < 3 {
			return os.ErrPermission
		}
		return nil
	})
	if err := Replace("src", "dst"); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestReplaceNonTransientReturnsImmediately(t *testing.T) {
	want := errors.New("invalid path")
	attempts := 0
	withRename(t, func(string, string) error {
		attempts++
		return want
	})
	sleep = func(time.Duration) { t.Fatal("non-transient error slept") }
	if err := Replace("src", "dst"); !errors.Is(err, want) {
		t.Fatalf("Replace error = %v, want %v", err, want)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestReplaceExhaustionReturnsLastError(t *testing.T) {
	attempts := 0
	var last error
	withRename(t, func(string, string) error {
		attempts++
		last = fmt.Errorf("rename %d: %w", attempts, os.ErrPermission)
		return last
	})
	started := now()
	var delays []time.Duration
	advance := sleep
	sleep = func(delay time.Duration) {
		delays = append(delays, delay)
		advance(delay)
	}
	if err := Replace("src", "dst"); err != last {
		t.Fatalf("Replace error = %v, want last error %v", err, last)
	}
	wantDelays := []time.Duration{25, 50, 100, 200, 250, 250, 250, 250, 250, 250, 125}
	if attempts != len(wantDelays)+1 || len(delays) != len(wantDelays) {
		t.Fatalf("attempts = %d, delays = %v", attempts, delays)
	}
	for i, want := range wantDelays {
		if delays[i] != want*time.Millisecond {
			t.Fatalf("delay %d = %s, want %s", i, delays[i], want*time.Millisecond)
		}
	}
	if elapsed := now().Sub(started); elapsed != 2*time.Second {
		t.Fatalf("retry window = %s, want 2s", elapsed)
	}
}

func TestReplaceDoesNotRetryErrno5OnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows error 5 is transient there")
	}
	attempts := 0
	withRename(t, func(string, string) error {
		attempts++
		return syscall.Errno(5)
	})
	if err := Replace("src", "dst"); err == nil {
		t.Fatal("Replace unexpectedly succeeded")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestReplaceRetriesWindowsSharingErrors(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows sharing errors are transient only on Windows")
	}
	attempts := 0
	withRename(t, func(string, string) error {
		attempts++
		switch attempts {
		case 1:
			return syscall.Errno(5)
		case 2:
			return syscall.Errno(32)
		default:
			return nil
		}
	})
	if err := Replace("src", "dst"); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}
