// Package fsretry provides bounded retries for atomic file replacement.
package fsretry

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"time"
)

var rename = os.Rename
var now = time.Now
var sleep = time.Sleep

// Replace atomically renames src over dst, retrying transient filesystem
// failures for a bounded period to accommodate short-lived Windows file locks.
func Replace(src, dst string) error {
	return ReplaceWith(rename, src, dst)
}

// ReplaceWith applies the same retry policy to a caller-supplied rename
// function, allowing packages to preserve their existing test seams.
func ReplaceWith(rename func(string, string) error, src, dst string) error {
	deadline := now().Add(2 * time.Second)
	delay := 25 * time.Millisecond
	for {
		err := rename(src, dst)
		if err == nil || !transient(err) {
			return err
		}
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return err
		}
		if delay > remaining {
			delay = remaining
		}
		sleep(delay)
		delay *= 2
		if delay > 250*time.Millisecond {
			delay = 250 * time.Millisecond
		}
	}
}

func transient(err error) bool {
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EBUSY) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == syscall.Errno(5) || errno == syscall.Errno(32))
}
