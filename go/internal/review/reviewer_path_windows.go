//go:build windows

package review

import "syscall"

func resolveReviewerPathAliases(path string) string {
	input, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	n, _ := syscall.GetLongPathName(input, nil, 0)
	if n == 0 {
		return path
	}
	buffer := make([]uint16, n)
	n, err = syscall.GetLongPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || int(n) > len(buffer) {
		return path
	}
	return syscall.UTF16ToString(buffer[:n])
}
