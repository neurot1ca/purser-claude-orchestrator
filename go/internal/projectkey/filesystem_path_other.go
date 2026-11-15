//go:build !windows && !darwin

package projectkey

import "os"

func filesystemPath(string) (string, error) { return "", os.ErrNotExist }
