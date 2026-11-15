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
	if absolute, err := filepath.Abs(repository); err == nil {
		repository = absolute
	}
	if resolved, err := filepath.EvalSymlinks(repository); err == nil {
		repository = resolved
	}
	if resolved, err := filesystemPath(repository); err == nil {
		repository = resolved
	}
	repository = filepath.Clean(repository)
	if runtime.GOOS == "windows" {
		repository = strings.ToLower(repository)
	}
	h := sha256.Sum256([]byte(repository))
	return hex.EncodeToString(h[:])[:16]
}
