package telemetryhealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"purser-hook/internal/fsretry"

	"purser-hook/internal/contextio"
)

const fileName = "telemetry_health.json"

var renameHealth = os.Rename

// Update records one component check in the shared state health file.
// Failure codes and path classes are bounded machine-readable values.
func Update(stateRoot, component, reason, pathClass string, failed bool) (err error) {
	if !validComponent(component) || (failed && !validCode(reason)) || !validPathClass(pathClass) {
		return errors.New("invalid health update")
	}
	root, err := filepath.Abs(stateRoot)
	if err != nil || !safeDirectory(root) {
		return errors.New("unsafe health state root")
	}
	dir := filepath.Join(root, "telemetry")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if !beneath(root, dir) || !safeDirectory(dir) {
		return errors.New("unsafe health directory")
	}
	path := filepath.Join(dir, fileName)
	if !beneath(root, path) || isLink(path) {
		return errors.New("unsafe health path")
	}
	unlock, err := contextio.LockFile(filepath.Join(dir, fileName+".lock"))
	if err != nil {
		return err
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			if err == nil {
				err = unlockErr
			} else {
				err = errors.Join(err, unlockErr)
			}
		}
	}()

	health := map[string]any{"schema_version": 1, "status": "OK", "failure_count": int64(0)}
	if data, readErr := os.ReadFile(path); readErr == nil {
		if err := json.Unmarshal(data, &health); err != nil || health == nil {
			return errors.New("invalid existing health file")
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	components, ok := health["components"].(map[string]any)
	if !ok {
		components = map[string]any{}
	}
	previous, _ := components[component].(map[string]any)
	if previous == nil {
		previous = map[string]any{"lifetime_failures": int64(0), "consecutive_failures": int64(0)}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	previous["component"] = component
	previous["path_class"] = pathClass
	previous["last_checked_at"] = now
	if failed {
		previous["status"] = "DEGRADED"
		previous["last_failure"] = map[string]any{"reason": reason, "path_class": pathClass, "at": now}
		previous["last_error_code"] = reason
		previous["lifetime_failures"] = number(previous["lifetime_failures"]) + 1
		previous["consecutive_failures"] = number(previous["consecutive_failures"]) + 1
	} else {
		previous["status"] = "OK"
		previous["last_successful_check"] = now
		previous["consecutive_failures"] = int64(0)
	}
	components[component] = previous
	health["schema_version"] = 1
	health["components"] = components
	health["updated_at"] = now
	health["lifetime_failure_count"] = sumFailures(components)
	health["consecutive_failure_count"] = sumConsecutive(components)
	health["status"] = aggregateStatus(components)
	if failed {
		health["last_error_code"] = reason
	}
	data, err := json.Marshal(health)
	if err != nil {
		return err
	}
	return publish(path, data)
}

func validCode(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func validComponent(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validPathClass(value string) bool {
	if len(value) == 0 || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func number(value any) int64 {
	switch n := value.(type) {
	case float64:
		if n >= 0 && n <= 1<<53 && n == float64(int64(n)) {
			return int64(n)
		}
	case json.Number:
		var i int64
		if _, err := fmt.Sscan(string(n), &i); err == nil && i >= 0 {
			return i
		}
	case int64:
		if n >= 0 {
			return n
		}
	case int:
		if n >= 0 {
			return int64(n)
		}
	}
	return 0
}

func sumFailures(values map[string]any) int64 {
	var n int64
	for _, value := range values {
		if c, ok := value.(map[string]any); ok {
			n += number(c["lifetime_failures"])
		}
	}
	return n
}
func sumConsecutive(values map[string]any) int64 {
	var n int64
	for _, value := range values {
		if c, ok := value.(map[string]any); ok {
			n += number(c["consecutive_failures"])
		}
	}
	return n
}
func aggregateStatus(values map[string]any) string {
	for _, value := range values {
		if c, ok := value.(map[string]any); ok && c["status"] == "DEGRADED" {
			return "DEGRADED"
		}
	}
	return "OK"
}

func beneath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isLink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}
func safeDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func publish(path string, data []byte) error {
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm() & 0600
		if mode == 0 {
			mode = 0600
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".telemetry_health.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	err = fsretry.ReplaceWith(renameHealth, tmpName, path)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, openErr := os.Open(filepath.Dir(path))
		if openErr == nil {
			err = dir.Sync()
			closeErr := dir.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	return err
}
