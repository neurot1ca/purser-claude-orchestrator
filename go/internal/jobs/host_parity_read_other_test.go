//go:build !windows

package jobs

import "os"

func readHostParityRecordBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}
