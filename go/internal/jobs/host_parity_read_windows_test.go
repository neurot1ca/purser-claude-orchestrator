package jobs

import (
	"io"
	"os"
	"syscall"
)

func readHostParityRecordBytes(path string) ([]byte, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	data, readErr := io.ReadAll(file)
	if closeErr := file.Close(); readErr == nil {
		readErr = closeErr
	}
	return data, readErr
}
