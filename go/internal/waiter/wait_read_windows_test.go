//go:build windows

package waiter_test

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

var (
	kernel32CreateFile  = syscall.NewLazyDLL("kernel32.dll").NewProc("CreateFileW")
	kernel32CloseHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("CloseHandle")
)

func holdJobRead(t *testing.T, path string) func() {
	t.Helper()
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("convert lock path to UTF-16: %v", err)
	}
	handle, _, err := kernel32CreateFile.Call(
		uintptr(unsafe.Pointer(name)), 0, 0, 0, 3, 0x80, 0,
	)
	runtime.KeepAlive(name)
	if handle == ^uintptr(0) {
		t.Fatalf("CreateFileW(%q): %v", path, err)
	}
	return func() {
		_, _, _ = kernel32CloseHandle.Call(handle)
	}
}
