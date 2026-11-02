package review

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"purser-hook/internal/fsretry"
)

// Resolve only the installed Cursor launcher and literal single-target shims.
// Arbitrary shell scripts cannot be resolved safely and remain inconclusive.
func cursorExecutableIdentity(executable, version string) (string, error) {
	pending := []string{executable}
	seen := map[string]bool{}
	identity := sha256.New()
	for len(pending) > 0 {
		path := pending[0]
		pending = pending[1:]
		var resolved string
		err := fsretry.Retry(func() error {
			var err error
			resolved, err = filepath.EvalSymlinks(path)
			return err
		}, 2*time.Second)
		if err != nil {
			return "", err
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		if len(seen) > 8 {
			return "", os.ErrInvalid
		}
		digest := sha256.New()
		var prefix [2]byte
		err = fsretry.Retry(func() error {
			digest.Reset()
			file, err := os.Open(resolved)
			if err != nil {
				return err
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return os.ErrInvalid
			}
			if _, err := file.ReadAt(prefix[:], 0); err != nil && err != io.EOF {
				return err
			}
			_, err = io.Copy(digest, file)
			return err
		}, 2*time.Second)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(identity, resolved+"\x00"+hex.EncodeToString(digest.Sum(nil))+"\x00")
		ext := strings.ToLower(filepath.Ext(resolved))
		unixScript := string(prefix[:]) == "#!"
		if !unixScript && (runtime.GOOS != "windows" || ext != ".cmd" && ext != ".ps1") {
			continue
		}
		var script []byte
		err = fsretry.Retry(func() error {
			var err error
			script, err = os.ReadFile(resolved)
			return err
		}, 2*time.Second)
		if err != nil {
			return "", err
		}
		scriptDigest := sha256.Sum256(script)
		if !strings.EqualFold(hex.EncodeToString(scriptDigest[:]), hex.EncodeToString(digest.Sum(nil))) {
			return "", os.ErrInvalid
		}
		text := string(script)
		dir := filepath.Dir(resolved)
		if unixScript {
			// Recognize a version-directory launcher; never guess an external runtime.
			match := regexp.MustCompile(`(?m)^\s*(?:exec\s+)?"\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?/node"\s+"\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?/([A-Za-z0-9._-]+\.m?js)"\s+"\$@"\s*$`).FindStringSubmatch(text)
			if len(match) != 4 || match[1] != match[2] {
				return "", os.ErrInvalid
			}
			assignment := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(match[1]) + `=([^\r\n]+)$`).FindStringSubmatch(text)
			if len(assignment) != 2 || !strings.Contains(assignment[1], "dirname") || !strings.Contains(assignment[1], "pwd") || !strings.Contains(assignment[1], "$0") && !strings.Contains(assignment[1], "${BASH_SOURCE[0]}") {
				return "", os.ErrInvalid
			}
			pending = append(pending, filepath.Join(dir, "node"), filepath.Join(dir, match[3]))
			continue
		}
		pending = append(pending, filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
		switch {
		case ext == ".cmd" && strings.Contains(text, `-File "%SCRIPT_DIR%\cursor-agent.ps1"`):
			pending = append(pending, filepath.Join(dir, "cursor-agent.ps1"))
		case ext == ".ps1" && strings.Contains(text, `& "$nodePath" "$scriptPath\versions\$versionName\index.js" $args`) && strings.Contains(text, `& "$scriptPath\node.exe" "$scriptPath\index.js" $args`):
			if _, err := os.Stat(filepath.Join(dir, "node.exe")); os.IsNotExist(err) {
				if version != filepath.Base(version) || strings.ContainsAny(version, `/\:`) {
					return "", os.ErrInvalid
				}
				dir = filepath.Join(dir, "versions", version)
			} else if err != nil {
				return "", err
			}
			pending = append(pending, filepath.Join(dir, "node.exe"), filepath.Join(dir, "index.js"))
		default:
			pattern := `(?is)^\s*@echo off\s*"([^"\r\n]+)"\s+%\*\s+exit /b %errorlevel%\s*$`
			if ext == ".ps1" {
				pattern = `(?is)^\s*& '([^'\r\n]+)' @args\s+exit \$LASTEXITCODE\s*$`
			}
			match := regexp.MustCompile(pattern).FindStringSubmatch(text)
			if len(match) != 2 || !filepath.IsAbs(match[1]) || strings.EqualFold(filepath.Ext(match[1]), ".cmd") || strings.EqualFold(filepath.Ext(match[1]), ".ps1") {
				return "", os.ErrInvalid
			}
			pending = append(pending, match[1])
		}
		// The installed shims may promote a staged version before launching.
		promote := filepath.Join(filepath.Dir(resolved), "versions", "promote-staged.cmd")
		if info, err := os.Stat(promote); err == nil && info.Mode().IsRegular() {
			// Hash this auxiliary script without treating it as an entry-point shim.
			var data []byte
			if err := fsretry.Retry(func() error { var e error; data, e = os.ReadFile(promote); return e }, 2*time.Second); err != nil {
				return "", err
			}
			sum := sha256.Sum256(data)
			_, _ = io.WriteString(identity, promote+"\x00"+hex.EncodeToString(sum[:])+"\x00")
		} else if err == nil || !os.IsNotExist(err) {
			if err == nil {
				return "", os.ErrInvalid
			}
			return "", err
		}
	}
	return hex.EncodeToString(identity.Sum(nil)), nil
}
