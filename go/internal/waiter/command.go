package waiter

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"purser-hook/internal/pluginroot"
	"purser-hook/internal/profile"
)

// Command returns the runnable command for the waiter of jobID.
func Command(jobID string, maxSeconds ...int64) string {
	executable := "purser-hook"
	if path, err := pluginroot.RuntimeExecutable(profile.StateRoot()); err == nil {
		if path, err = filepath.Abs(path); err == nil {
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				executable = resolved
			}
		}
	}
	token := "unknown"
	if owner, err := SessionOwner(); err == nil {
		token = owner.String()
	}
	seconds := int64(0)
	if len(maxSeconds) > 0 {
		seconds = maxSeconds[0]
	}
	return commandForPlatform(executable, jobID, token, seconds, runtime.GOOS == "windows")
}

func commandForPlatform(executable, jobID, token string, maxSeconds int64, windows bool) string {
	command := `"` + executable + `" wait ` + jobID + ` --session-owner ` + token
	if windows {
		command = "& " + powerShellQuote(executable) + " wait " + jobID + " --session-owner " + token
	}
	if maxSeconds > 0 {
		command += " --max-seconds " + strconv.FormatInt(maxSeconds, 10)
	}
	return command
}

// ValidationCommand returns a shell-ready command for the bounded apply-validation waiter.
func ValidationCommand(stateRoot, repositoryKey, jobID string) string {
	executable := "purser-hook"
	if path, err := pluginroot.RuntimeExecutable(stateRoot); err == nil {
		if path, err = filepath.Abs(path); err == nil {
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				executable = resolved
			}
		}
	}
	if runtime.GOOS == "windows" {
		return "& " + powerShellQuote(executable) + " wait-validation " + powerShellQuote(stateRoot) + " " + powerShellQuote(repositoryKey) + " " + powerShellQuote(jobID)
	}
	return shellQuote(executable) + " wait-validation " + shellQuote(stateRoot) + " " + shellQuote(repositoryKey) + " " + shellQuote(jobID)
}

func powerShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
