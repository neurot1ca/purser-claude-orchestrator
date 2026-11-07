package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var agyModel = regexp.MustCompile(`^gemini-[A-Za-z0-9._-]+$`)
var cursorModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// ResolveAgentPrefix mirrors Python's alternate-agent binary override rules.
func ResolveAgentPrefix(agent string, override any) ([]string, error) {
	if override != nil {
		if value, ok := override.(string); ok {
			if strings.HasPrefix(value, "[") {
				var parsed []string
				if err := json.Unmarshal([]byte(value), &parsed); err != nil || len(parsed) == 0 {
					return nil, errors.New("invalid agent binary override")
				}
				for _, item := range parsed {
					if item == "" {
						return nil, errors.New("invalid agent binary override")
					}
				}
				return parsed, nil
			}
			if value != "" {
				return []string{value}, nil
			}
		}
		if values, ok := override.([]any); ok {
			prefix := make([]string, 0, len(values))
			for _, value := range values {
				item, valid := value.(string)
				if !valid || item == "" {
					return nil, errors.New("invalid agent binary override")
				}
				prefix = append(prefix, item)
			}
			if len(prefix) > 0 {
				return prefix, nil
			}
		}
		return nil, errors.New("invalid agent binary override")
	}
	lookup := agent
	if agent == "cursor" {
		lookup = "cursor-agent"
		if runtime.GOOS == "windows" {
			lookup += ".cmd"
		}
	}
	launcher, err := exec.LookPath(lookup)
	if err != nil {
		return nil, err
	}
	if agent != "opencode" {
		return []string{launcher}, nil
	}
	packages := map[string][]string{
		"windows": {"opencode-windows-x64", "opencode-windows-x64-baseline"},
		"linux":   {"opencode-linux-x64", "opencode-linux-arm64"},
		"darwin":  {"opencode-darwin-x64", "opencode-darwin-arm64"},
	}[runtime.GOOS]
	roots := []string{
		filepath.Join(filepath.Dir(launcher), "node_modules", "opencode-ai", "node_modules"),
		filepath.Join(filepath.Dir(filepath.Dir(launcher)), "node_modules", "opencode-ai", "node_modules"),
		filepath.Join(filepath.Dir(filepath.Dir(launcher)), "node_modules"),
		filepath.Join(filepath.Dir(filepath.Dir(launcher)), "opencode-ai", "node_modules"),
	}
	binary := "opencode"
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	for _, root := range roots {
		for _, name := range packages {
			candidate := filepath.Join(root, name, "bin", binary)
			if _, err := exec.LookPath(candidate); err == nil {
				return []string{candidate}, nil
			}
		}
	}
	if runtime.GOOS == "windows" && strings.ToLower(filepath.Ext(launcher)) != ".exe" {
		return nil, errors.New("opencode executable was not found")
	}
	return []string{launcher}, nil
}

// AgentCommandInput contains the values used to construct an alternate-agent
// CLI invocation. Prefix and Args remain argv elements; no shell is involved.
type AgentCommandInput struct {
	Agent      string
	Prefix     []string
	Tool       string
	CWD        string
	Model      string
	ConfigArgs []string
	Sandbox    *string
	ThreadID   string
	Prompt     string
}

// OpenCodeDirectory returns the canonical launch directory after confirming it
// exists and, when assigned, is inside the job worktree.
func OpenCodeDirectory(worktree, cwd string) (string, error) {
	if cwd == "" {
		return "", errors.New("OpenCode directory is unavailable")
	}
	resolve := func(path string) (string, error) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return "", errors.New("OpenCode directory is unavailable")
		}
		return filepath.Clean(resolved), nil
	}
	directory, err := resolve(cwd)
	if err != nil {
		return "", errors.New("OpenCode directory is unavailable")
	}
	if worktree == "" {
		return directory, nil
	}
	root, err := resolve(worktree)
	if err != nil {
		return "", errors.New("OpenCode worktree is unavailable")
	}
	if !strings.EqualFold(filepath.VolumeName(root), filepath.VolumeName(directory)) {
		return "", errors.New("OpenCode directory is outside the assigned worktree")
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("OpenCode directory is outside the assigned worktree")
	}
	return directory, nil
}

const openCodeReadOnlyAgent = "purser-read-only"

// AgentCommandEnvironment adds launch-time policy that cannot be expressed in
// argv. OpenCode reads OPENCODE_CONFIG_CONTENT before resolving the agent.
func AgentCommandEnvironment(in AgentCommandInput) []string {
	if in.Agent != "opencode" || in.Sandbox == nil || *in.Sandbox != "read-only" {
		return nil
	}
	permissions := map[string]string{"edit": "deny", "write": "deny", "bash": "deny", "webfetch": "deny", "task": "deny", "external_directory": "deny", "*_*": "deny", "opencode_list_mcp_resources": "deny", "opencode_read_mcp_resource": "deny"}
	config, _ := json.Marshal(map[string]any{
		"mcp":        openCodeDisabledMCPServers(in.CWD),
		"permission": permissions,
		"agent":      map[string]any{openCodeReadOnlyAgent: map[string]any{"mode": "primary", "permission": permissions}},
	})
	return []string{"OPENCODE_CONFIG_CONTENT=" + string(config)}
}

func openCodeDisabledMCPServers(cwd string) map[string]any {
	dirs := []string{}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "opencode"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", "opencode"))
	}
	for _, env := range []string{"USERPROFILE", "APPDATA"} {
		if value := os.Getenv(env); value != "" {
			if env == "USERPROFILE" {
				dirs = append(dirs, filepath.Join(value, ".config", "opencode"))
			}
			if env == "APPDATA" {
				dirs = append(dirs, filepath.Join(value, "opencode"))
			}
		}
	}
	var paths []string
	seen := map[string]bool{}
	for _, dir := range dirs {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			path := filepath.Join(dir, name)
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	if cwd != "" {
		for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
			for _, base := range []string{dir, filepath.Join(dir, ".opencode")} {
				for _, name := range []string{"opencode.json", "opencode.jsonc"} {
					path := filepath.Join(base, name)
					if !seen[path] {
						seen[path] = true
						paths = append(paths, path)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil || filepath.Dir(dir) == dir {
				break
			}
		}
	}
	servers := map[string]any{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var config map[string]any
		if json.Unmarshal(stripOpenCodeJSONC(data), &config) != nil {
			continue
		}
		mcp, _ := config["mcp"].(map[string]any)
		for name := range mcp {
			servers[name] = map[string]any{"enabled": false}
		}
	}
	return servers
}

func stripOpenCodeJSONC(data []byte) []byte {
	var out strings.Builder
	out.Grow(len(data))
	inString, escaped, lineComment, blockComment := false, false, false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if lineComment {
			if c == '\n' {
				lineComment = false
				out.WriteByte(c)
			}
			continue
		}
		if blockComment {
			if c == '*' && i+1 < len(data) && data[i+1] == '/' {
				blockComment = false
				i++
			}
			continue
		}
		if inString {
			out.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
		} else if c == '/' && i+1 < len(data) && data[i+1] == '/' {
			lineComment = true
			i++
		} else if c == '/' && i+1 < len(data) && data[i+1] == '*' {
			blockComment = true
			i++
		} else {
			out.WriteByte(c)
		}
	}
	data = []byte(out.String())
	clean := data[:0]
	inString, escaped = false, false
	for i, c := range data {
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
		} else if c == '"' {
			inString = true
		} else if c == ',' {
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\r' || data[j] == '\n') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}
		clean = append(clean, c)
	}
	return clean
}

// BuildAgentCommand maps the OpenCode and Antigravity bridge options to argv.
func BuildAgentCommand(in AgentCommandInput) ([]string, error) {
	// workspace-write is Codex's name for every bridged coder's default mode.
	if in.Sandbox != nil && *in.Sandbox == "workspace-write" {
		in.Sandbox = nil
	}
	switch in.Agent {
	case "cursor":
		if in.Model == "" || in.Model != "auto" && !cursorModel.MatchString(in.Model) {
			return nil, errors.New("Cursor model must be a concrete model ID")
		}
		if in.Sandbox != nil && *in.Sandbox != "read-only" {
			return nil, errors.New("Cursor sandbox has no verified equivalent")
		}
		if len(in.ConfigArgs) > 1 || len(in.ConfigArgs) == 1 && in.ConfigArgs[0] != `model_reasoning_effort="none"` {
			return nil, errors.New("unsupported Cursor config")
		}
		command := append([]string{}, in.Prefix...)
		command = append(command, "--print", "--output-format", "stream-json", "--model", in.Model)
		if in.Sandbox != nil {
			command = append(command, "--mode", "ask", "--trust")
		} else {
			command = append(command, "--force")
		}
		if in.Tool == "codex-reply" {
			if in.ThreadID == "" {
				return nil, errors.New("Cursor reply requires a thread ID")
			}
			command = append(command, "--resume", in.ThreadID)
		}
		return command, nil
	case "agy":
		if !agyModel.MatchString(in.Model) {
			return nil, errors.New("Antigravity model must be a Gemini model from agy models")
		}
		if in.Sandbox != nil && *in.Sandbox != "read-only" || len(in.ConfigArgs) != 1 || len(in.ConfigArgs[0]) <= len("model_reasoning_effort=") || in.ConfigArgs[0][:len("model_reasoning_effort=")] != "model_reasoning_effort=" {
			return nil, errors.New("unsupported Antigravity sandbox or config")
		}
		var effort string
		if err := json.Unmarshal([]byte(in.ConfigArgs[0][len("model_reasoning_effort="):]), &effort); err != nil || (effort != "low" && effort != "medium" && effort != "high" && effort != "max") {
			return nil, errors.New("Antigravity effort must be low, medium, high or max")
		}
		command := append([]string{}, in.Prefix...)
		prompt := in.Prompt + "\nRun every command in the foreground and wait for it; never start background tasks."
		command = append(command, "--print", prompt, "--output-format", "json", "--model", in.Model)
		if in.Sandbox != nil {
			command = append(command, "--mode", "plan", "--sandbox")
		} else {
			command = append(command, "--mode", "accept-edits", "--dangerously-skip-permissions")
		}
		command = append(command, "--effort", effort)
		if in.Tool == "codex-reply" {
			command = append(command, "--conversation", in.ThreadID)
		} else {
			command = append(command, "--new-project", "--add-dir", in.CWD)
		}
		return command, nil
	case "claude":
		if !claudeModel.MatchString(in.Model) {
			return nil, errors.New("Claude model is invalid")
		}
		effort := ""
		effortSet := false
		for _, arg := range in.ConfigArgs {
			const key = "model_reasoning_effort="
			if !strings.HasPrefix(arg, key) || effortSet {
				return nil, errors.New("unsupported Claude config")
			}
			effortSet = true
			if err := json.Unmarshal([]byte(strings.TrimPrefix(arg, key)), &effort); err != nil {
				return nil, errors.New("Claude effort is invalid")
			}
		}
		if effort != "" && effort != "none" && !claudeEfforts[effort] {
			return nil, errors.New("Claude effort is required and sandbox is unsupported")
		}
		readOnly := in.Sandbox != nil && *in.Sandbox == "read-only"
		if in.Sandbox != nil && !readOnly {
			return nil, errors.New("Claude effort is required and sandbox is unsupported")
		}
		command := append([]string{}, in.Prefix...)
		command = append(command, "--setting-sources", "")
		tools := "Read,Glob,Grep"
		if readOnly {
			command = append(command, "--restricted", "--safe-mode", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-chrome")
		} else {
			tools = "Read,Edit,Write,Glob,Grep,Bash"
			// Worktrees live under ~/.claude, which only bypassPermissions may edit,
			// and restricted mode rejects bypassPermissions.
			command = append(command, "--strict-mcp-config", "--permission-mode", "bypassPermissions")
		}
		command = append(command, "--print", "--output-format", "json", "--model", in.Model)
		if effort != "" && effort != "none" {
			command = append(command, "--effort", effort)
		}
		command = append(command, "--tools", tools)
		if in.Tool == "codex-reply" {
			if in.ThreadID == "" {
				return nil, errors.New("Claude reply requires a thread ID")
			}
			command = append(command, "--resume", in.ThreadID)
		}
		// "--" ends the variadic --tools list so the prompt stays positional.
		return append(command, "--", in.Prompt), nil
	case "opencode":
		if in.Model == "" || !strings.Contains(in.Model, "/") || in.Model[0] == '/' || in.Model[len(in.Model)-1] == '/' {
			return nil, errors.New("OpenCode model must be provider/model")
		}
		if in.Sandbox != nil && *in.Sandbox != "read-only" {
			return nil, errors.New("OpenCode sandbox has no verified equivalent")
		}
		variants := make([]string, 0, 1)
		for _, arg := range in.ConfigArgs {
			const prefix = "model_reasoning_effort="
			if len(arg) >= len(prefix) && arg[:len(prefix)] == prefix {
				variants = append(variants, arg[len(prefix):])
			}
		}
		if len(variants) > 1 || len(variants) != len(in.ConfigArgs) {
			return nil, errors.New("unsupported OpenCode config")
		}
		command := append([]string{}, in.Prefix...)
		directory, err := filepath.Abs(in.CWD)
		if err != nil || in.CWD == "" {
			return nil, errors.New("OpenCode directory is unavailable")
		}
		command = append(command, "run", "--format", "json", "--dir", directory, "--model", in.Model)
		if in.Sandbox != nil {
			command = append(command, "--agent", openCodeReadOnlyAgent)
		}
		if len(variants) != 0 {
			var variant string
			if err := json.Unmarshal([]byte(variants[0]), &variant); err != nil || variant == "" {
				return nil, errors.New("OpenCode variant must be a nonempty string")
			}
			command = append(command, "--variant", variant)
		}
		if in.Tool == "codex-reply" {
			command = append(command, "--session", in.ThreadID)
		}
		command = append(command, "--", in.Prompt)
		return command, nil
	default:
		return nil, fmt.Errorf("unsupported agent: %s", in.Agent)
	}
}

var claudeModel = regexp.MustCompile(`^(claude-[A-Za-z0-9._-]+|haiku|sonnet|opus)$`)
var claudeEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// ValidateAgentResume rejects cross-agent threads and, for AGY, a resumed
// conversation whose returned identity differs from the requested thread.
func ValidateAgentResume(agent, recordedAgent, requestedThread, resumedIdentity string) error {
	if recordedAgent == "" {
		recordedAgent = "codex"
	}
	if recordedAgent != agent {
		return errors.New("thread belongs to a different agent")
	}
	if agent == "agy" && resumedIdentity != requestedThread {
		return errors.New("Antigravity did not resume the requested conversation; start a fresh call")
	}
	if agent == "cursor" && resumedIdentity != requestedThread {
		return errors.New("Cursor did not resume the requested session; start a fresh call")
	}
	return nil
}
