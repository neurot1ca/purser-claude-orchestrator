package pluginroot

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// RuntimeExecutable keeps long-lived children outside the replaceable plugin
// directory. Copy failures preserve the original executable and launch behavior.
func RuntimeExecutable(stateRoot string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return executable, err
	}
	return RuntimeCopy(stateRoot, executable), nil
}

// RuntimeCopy also supports launchers that resolve a separate hook binary.
func RuntimeCopy(stateRoot, executable string) string {
	source, err := os.Open(executable)
	if err != nil {
		return executable
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return executable
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, source); err != nil {
		return executable
	}
	dir, err := filepath.Abs(filepath.Join(stateRoot, "runtime", fmt.Sprintf("%x", hash.Sum(nil)[:16])))
	if err != nil {
		return executable
	}
	target := filepath.Join(dir, filepath.Base(executable))
	ready := func() bool {
		cached, err := os.Lstat(target)
		return err == nil && cached.Mode().IsRegular() && cached.Size() == info.Size() &&
			(runtime.GOOS == "windows" || cached.Mode().Perm()&0111 == 0111)
	}
	if ready() {
		return target
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return executable
	}
	temp, err := os.CreateTemp(dir, ".copy-")
	if err != nil {
		return executable
	}
	defer os.Remove(temp.Name())
	_, err = source.Seek(0, io.SeekStart)
	if err == nil {
		_, err = io.Copy(temp, source)
	}
	if err == nil {
		err = temp.Chmod(0755)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil || closeErr != nil {
		return executable
	}
	for attempt := 0; attempt < 4; attempt++ {
		// Another hook may have published the same copy while we were writing.
		if ready() || os.Rename(temp.Name(), target) == nil {
			return target
		}
		if runtime.GOOS != "windows" {
			break
		}
		if attempt < 3 {
			time.Sleep(25 * time.Millisecond)
		}
	}
	return executable
}

// PruneRuntime removes only older copies after the normal retention interval.
// In-use Windows executables may resist deletion; the next sweep retries them.
func PruneRuntime(stateRoot, current string, now time.Time) {
	root, err := filepath.Abs(filepath.Join(stateRoot, "runtime"))
	if err != nil || filepath.Dir(filepath.Dir(current)) != root {
		return
	}
	currentDir := filepath.Dir(current)
	currentInfo, err := os.Stat(currentDir)
	if err != nil {
		return
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err == nil && entry.IsDir() && path != currentDir &&
			info.ModTime().Before(currentInfo.ModTime()) && now.Sub(info.ModTime()) >= 72*time.Hour {
			_ = os.RemoveAll(path)
		}
	}
}
