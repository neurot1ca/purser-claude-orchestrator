package projectkey

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"runtime"
	"strings"
)

// For returns the stable per-project key, resolving aliases when possible.
func For(repository string) string {
	repository = filepath.Clean(repository)
	resolvedPath := repository
	if absolute, err := filepath.Abs(repository); err == nil {
		resolvedPath = absolute
	}
	resolved := false
	if path, err := filepath.EvalSymlinks(resolvedPath); err == nil {
		resolvedPath = path
		resolved = true
	}
	if path, err := filesystemPath(resolvedPath); err == nil {
		resolvedPath = path
		resolved = true
	}
	if resolved {
		repository = resolvedPath
	}
	repository = filepath.Clean(repository)
	if runtime.GOOS == "windows" {
		repository = strings.ToLower(repository)
	}
	h := sha256.Sum256([]byte(repository))
	return hex.EncodeToString(h[:])[:16]
}
