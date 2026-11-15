//go:build windows

package projectkey

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var getFinalPathNameByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")

func filesystemPath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	buffer := make([]uint16, 256)
	for {
		result, _, callErr := getFinalPathNameByHandle.Call(file.Fd(), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
		if result == 0 {
			return "", callErr
		}
		if result < uintptr(len(buffer)) {
			path = syscall.UTF16ToString(buffer[:result])
			break
		}
		buffer = make([]uint16, result+1)
	}
	if strings.HasPrefix(path, `\\?\UNC\`) {
		path = `\\` + path[len(`\\?\UNC\`):]
	} else if strings.HasPrefix(path, `\\?\`) {
		path = path[4:]
	}
	return path, nil
}
