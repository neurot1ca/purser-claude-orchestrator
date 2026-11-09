package enforce

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"purser-hook/internal/proc"
	"purser-hook/internal/profile"
	"purser-hook/internal/projectkey"
)

const editDelegationReason = "Delegate this code change to the selected coder through the purser:dispatcher agent or codex_start."

func editTool(tool string) bool {
	return tool == "Edit" || tool == "Write" || tool == "MultiEdit" || tool == "NotebookEdit"
}

const editSourceExtensions = ".go .py .pyi .js .jsx .mjs .cjs .ts .tsx .mts .cts .rs .c .h .cc .cpp .cxx .hpp .cs .java .kt .kts .swift .m .mm .rb .php .scala .sc .sh .bash .zsh .ps1 .psm1 .bat .cmd .sql .r .R .lua .pl .ex .exs .erl .hrl .hs .fs .fsx .clj .cljs .dart .vue .svelte .html .css .scss .sass .less .ipynb"

type editCandidate struct {
	path        string
	requiresGit bool
}

// editDelegation is deliberately independent of MCP admission: every error allows.
func editDelegation(event map[string]any, state string) (response map[string]any, err error) {
	defer func() {
		if value := recover(); value != nil {
			response, err = nil, fmt.Errorf("edit delegation: %v", value)
		}
	}()
	if stringValue(event["agent_id"]) != "" || os.Getenv("PURSER_WORKER") == "1" {
		return nil, nil
	}
	input := mapValue(event["tool_input"])
	name := stringValue(input["file_path"])
	if event["tool_name"] == "NotebookEdit" {
		name = stringValue(input["notebook_path"])
	}
	if name == "" || !editSourcePath(name) {
		return nil, nil
	}
	if event["tool_name"] == "Edit" {
		old, oldOK := input["old_string"].(string)
		newText, newOK := input["new_string"].(string)
		if oldOK && newOK && old != "" && !strings.ContainsAny(old+newText, "\r\n") {
			if input["replace_all"] != true {
				return nil, nil
			}
			path := name
			if !filepath.IsAbs(path) {
				path = filepath.Join(stringValue(event["cwd"]), path)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			lines := 0
			for _, line := range strings.Split(string(content), "\n") {
				if strings.Contains(line, old) {
					lines++
				}
			}
			if lines <= 1 {
				return nil, nil
			}
		}
	}
	return editDelegationTargets(event, state, []editCandidate{{path: name}})
}

func editSourcePath(name string) bool {
	return strings.Contains(" "+editSourceExtensions+" ", " "+filepath.Ext(name)+" ")
}

func editDelegationTargets(event map[string]any, state string, candidates []editCandidate) (response map[string]any, err error) {
	defer func() {
		if value := recover(); value != nil {
			response, err = nil, fmt.Errorf("edit delegation: %v", value)
		}
	}()
	if stringValue(event["agent_id"]) != "" || os.Getenv("PURSER_WORKER") == "1" {
		return nil, nil
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	cwd := stringValue(event["cwd"])
	if !filepath.IsAbs(cwd) || stringValue(event["session_id"]) == "" {
		return nil, errors.New("edit delegation identity unavailable")
	}
	// Find the candidate repository without launching git; no git before roles.
	repo, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(repo, ".git")); statErr == nil {
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
		parent := filepath.Dir(repo)
		if parent == repo {
			return nil, nil
		}
		repo = parent
	}
	project := projectkey.For(repo)
	raw, err := os.ReadFile(filepath.Join(state, "projects", project, "roles.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rolesConfig, _, err := profile.DecodeTOML(raw)
	if err != nil {
		return nil, err
	}
	roles := mapValue(rolesConfig["roles"])
	if flag, exists := roles["enforce_delegation"]; exists {
		enabled, valid := flag.(bool)
		if !valid {
			return nil, errors.New("enforce_delegation must be boolean")
		}
		if !enabled {
			return nil, nil
		}
	}
	coder := strings.ToLower(strings.TrimSpace(stringValue(roles["coder"])))
	if coder == "" || coder == "none" || coder == "claude" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	git := func(args ...string) ([]byte, error) {
		cmd := proc.Hide(exec.CommandContext(ctx, "git", append([]string{"-c", "core.longpaths=true", "-C", repo}, args...)...))
		return cmd.Output()
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	a, err := os.Stat(strings.TrimSpace(string(root)))
	if err != nil {
		return nil, err
	}
	b, err := os.Stat(repo)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(a, b) {
		return nil, errors.New("git repository mismatch")
	}
	for _, candidate := range candidates {
		name := candidate.path
		if !editSourcePath(name) {
			continue
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		target, err := editResolvedPath(name)
		if err != nil {
			return nil, err
		}
		relative, inside := editRelative(repo, target)
		if !inside {
			continue
		}
		_, ignoreErr := git("check-ignore", "-q", "--no-index", "--", filepath.ToSlash(relative))
		if ignoreErr == nil {
			continue
		}
		var exit *exec.ExitError
		if !errors.As(ignoreErr, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil {
			return nil, ignoreErr
		}
		if candidate.requiresGit {
			if _, trackedErr := git("ls-files", "--error-unmatch", "--", filepath.ToSlash(relative)); trackedErr != nil {
				continue
			}
		}
		allowed, err := editRecovery(ctx, state, project, stringValue(event["session_id"]))
		if err != nil || allowed {
			return nil, err
		}
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": editDelegationReason}}, nil
	}
	return nil, nil
}

// Resolve existing ancestors for new Write targets, but never guess past a
// dangling symlink, permission error, or other unresolved filesystem object.
func editResolvedPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	base, err := editResolvedPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, filepath.Base(path)), nil
}

// SameFile respects volume case rules (including macOS), drive aliases and
// Windows long-path prefixes without folding distinct paths on Linux.
func editRelative(repo, target string) (string, bool) {
	root, err := os.Stat(repo)
	if err != nil {
		return "", false
	}
	relative := ""
	for path := target; ; path = filepath.Dir(path) {
		if info, err := os.Stat(path); err == nil && os.SameFile(root, info) {
			return relative, relative != ""
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", false
		}
		relative = filepath.Join(filepath.Base(path), relative)
	}
}

// Reuse the durable async index and job records, never create bypass state.
func editRecovery(ctx context.Context, state, project, session string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(state, "async_jobs"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var indexes []os.FileInfo
	for _, entry := range entries {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return false, err
		}
		indexes = append(indexes, info)
	}
	sort.Slice(indexes, func(i, j int) bool {
		if indexes[i].ModTime().Equal(indexes[j].ModTime()) {
			return indexes[i].Name() < indexes[j].Name()
		}
		return indexes[i].ModTime().After(indexes[j].ModTime())
	})
	// Do not infer recovery from an incomplete group tied at the scan boundary.
	cutoffTied := len(indexes) > 256 && indexes[255].ModTime().Equal(indexes[256].ModTime())
	if len(indexes) > 256 {
		indexes = indexes[:256]
	}
	var latest time.Time
	latestFailed, conflict := false, false
	for _, entry := range indexes {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		index, err := readAsyncObject(filepath.Join(state, "async_jobs", entry.Name()))
		if err != nil {
			return false, err
		}
		if index["project_key"] != project || index["session_id"] != session {
			continue
		}
		id := stringValue(index["jobId"])
		if !asyncHash.MatchString(id) {
			return false, errors.New("invalid async job ID")
		}
		job, err := readAsyncObject(filepath.Join(state, "codex_exec_mcp", "jobs", id+".json"))
		if err != nil {
			return false, err
		}
		if job["jobId"] != id || job["call"] != index["call"] {
			return false, errors.New("async job mismatch")
		}
		if job["applyState"] == "conflict" || job["applyConflictReason"] == "worker_replay" {
			conflict = true
		}
		failed := job["state"] == "FAILED" || job["state"] == "TIMED_OUT" || index["stage"] == "FAILED"
		if latest.IsZero() {
			latest, latestFailed = entry.ModTime(), failed
		} else if entry.ModTime().Equal(latest) {
			latestFailed = latestFailed && failed
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return conflict || latestFailed && !(cutoffTied && latest.Equal(indexes[len(indexes)-1].ModTime())), nil
}
