//go:build windows

package projectkey

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

func TestForWindowsShortName(t *testing.T) {
	long := filepath.Join(t.TempDir(), "project key short name")
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	input, err := syscall.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	getShortPathName := syscall.NewLazyDLL("kernel32.dll").NewProc("GetShortPathNameW")
	buffer := make([]uint16, 1024)
	result, _, callErr := getShortPathName.Call(uintptr(unsafe.Pointer(input)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if result == 0 {
		t.Skipf("8.3 short names are unavailable: %v", callErr)
	}
	short := syscall.UTF16ToString(buffer[:result])
	if short == long {
		t.Skip("the volume did not assign an 8.3 short name")
	}
	if For(long) != For(short) {
		t.Fatalf("short name changed key: For(%q) = %q, For(%q) = %q", long, For(long), short, For(short))
	}
}

func TestForWindowsJunctionAlias(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create directory junction: %v: %s", err, output)
	}
	if For(target) != For(link) {
		t.Fatalf("junction alias changed key: For(%q) = %q, For(%q) = %q", target, For(target), link, For(link))
	}
}
