//go:build darwin

package projectkey

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

func filesystemPath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	buffer := make([]byte, 4096)
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), 50, uintptr(unsafe.Pointer(&buffer[0]))) // F_GETPATH
	if errno != 0 {
		return "", errno
	}
	if end := bytes.IndexByte(buffer, 0); end >= 0 {
		buffer = buffer[:end]
	}
	return string(buffer), nil
}
