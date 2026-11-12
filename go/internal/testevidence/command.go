package testevidence

import (
	"regexp"
	"strings"
)

var shellFlag = regexp.MustCompile(`(?i)\s(?:-command|-lc|-c|/c)\s+`)
var exitOnFailureGuard = regexp.MustCompile(`(?i)^\s*if\s*\(\s*\$LASTEXITCODE\s+-ne\s+0\s*\)\s*\{\s*exit\s+\$LASTEXITCODE\s*\}\s*$`)

// Limit preserves an unknown status when bounding the evidence to six runs.
func Limit(runs []map[string]any) []map[string]any {
	if len(runs) <= 6 {
		return runs
	}
	tail := runs[len(runs)-6:]
	for _, run := range tail {
		if run["exit_code"] == nil {
			return tail
		}
	}
	for i := len(runs) - 7; i >= 0; i-- {
		if runs[i]["exit_code"] == nil {
			return append([]map[string]any{runs[i]}, tail[1:]...)
		}
	}
	return tail
}

func unwrap(command string) string {
	words := shellWords(command)
	if len(words) == 0 {
		return command
	}
	shell := strings.ToLower(words[0])
	if i := strings.LastIndexAny(shell, `/\\`); i >= 0 {
		shell = shell[i+1:]
	}
	shell = strings.TrimSuffix(shell, ".exe")
	switch shell {
	case "pwsh", "powershell", "cmd", "sh", "bash", "zsh":
		if loc := shellFlag.FindStringIndex(command); loc != nil {
			inner := strings.TrimSpace(command[loc[1]:])
			if len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"') {
				inner = unwrapShellArgument(inner)
			}
			return inner
		}
	}
	return command
}

func unwrapShellArgument(argument string) string {
	var word strings.Builder
	for len(argument) > 0 && (argument[0] == '\'' || argument[0] == '"') {
		quote := argument[0]
		end := strings.IndexByte(argument[1:], quote)
		if end < 0 {
			return argument
		}
		word.WriteString(argument[1 : end+1])
		argument = argument[end+2:]
		if len(argument) == 0 || argument[0] != '\'' && argument[0] != '"' {
			word.WriteString(argument)
			return word.String()
		}
	}
	return argument
}

// ExitCode keeps recognized runs even when their status cannot prove success.
// Nonzero compound-command exits remain failures; ambiguous successes are unknown.
func ExitCode(command string, raw any) (string, any) {
	target := Target(command)
	if target == "" {
		return "", nil
	}
	code, ok := raw.(float64)
	if !ok || code != float64(int(code)) {
		return target, nil
	}
	if code != 0 {
		return target, int(code)
	}
	if strings.Contains(command, `\"`) || strings.Contains(command, `\'`) || strings.ContainsRune(command, '`') {
		return target, nil
	}
	segments, separators, reliable := shellSegments(unwrap(command))
	if !reliable {
		return target, nil
	}
	seenTest := false
	for i, segment := range segments {
		isTest := Target(segment) != ""
		seenTest = seenTest || isTest
		if i == len(segments)-1 && !isTest || seenTest && !isTest || i < len(separators) && separators[i] != "&&" && (seenTest || separators[i] != ";") {
			return target, nil
		}
		if !seenTest && exitOnFailureGuard.MatchString(segment) {
			continue
		}
		words := shellWords(segment)
		if !isTest {
			if len(words) == 0 {
				return target, nil
			}
			switch strings.ToLower(words[0]) {
			case "gofmt", "goimports", "go", "cd", "set-location", "push-location", "mkdir":
			default:
				return target, nil
			}
		}
		for _, word := range words {
			if strings.EqualFold(word, "if") || strings.Contains(strings.ToLower(word), "$lastexitcode") {
				return target, nil
			}
		}
	}
	return target, 0
}

func shellSegments(command string) ([]string, []string, bool) {
	var segments []string
	var separators []string
	var quote rune
	reliable := true
	start := 0
	for i := 0; i < len(command); i++ {
		// Keep the exact PowerShell guard intact for ExitCode's setup check.
		if i == start {
			if end := strings.IndexByte(command[start:], '}'); end >= 0 && exitOnFailureGuard.MatchString(command[start:start+end+1]) {
				i = start + end
				continue
			}
		}
		r := rune(command[i])
		if r == '\n' || r == '\r' {
			reliable = false
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == '#' {
			reliable = false
		}
		if strings.ContainsRune(";&|\n\r<>`(){}", r) {
			segments = append(segments, command[start:i])
			separator := string(r)
			if r == '&' && i+1 < len(command) && command[i+1] == '&' {
				separator = "&&"
				i++
			} else if r == '|' && i+1 < len(command) && command[i+1] == '|' {
				separator = "||"
				i++
			}
			separators = append(separators, separator)
			if separator != ";" && separator != "&&" {
				reliable = false
			}
			start = i + 1
		}
	}
	return append(segments, command[start:]), separators, reliable && quote == 0
}

// Target returns the test-runner command within a shell invocation.
func Target(command string) string {
	words := shellWords(command)
	if len(words) == 0 {
		return ""
	}
	base := func(path string) string {
		path = strings.TrimSuffix(strings.ToLower(path), ".exe")
		if i := strings.LastIndexAny(path, `/\\`); i >= 0 {
			path = path[i+1:]
		}
		return path
	}
	inner := unwrap(command)
	segments, _, _ := shellSegments(inner)
	var targets []string
	for _, segment := range segments {
		tokens := shellWords(segment)
		for len(tokens) > 0 {
			if strings.HasPrefix(tokens[0], "$env:") && len(tokens) >= 3 && tokens[1] == "=" {
				tokens = tokens[3:]
				continue
			}
			if strings.Contains(tokens[0], "=") && !strings.HasPrefix(tokens[0], "=") {
				tokens = tokens[1:]
				continue
			}
			break
		}
		if len(tokens) == 0 {
			continue
		}
		cmd := base(tokens[0])
		if cmd == "go" && len(tokens) > 1 && tokens[1] == "test" ||
			cmd == "pytest" || cmd == "uv" && len(tokens) > 2 && tokens[1] == "run" && base(tokens[2]) == "pytest" ||
			(cmd == "npm" || cmd == "pnpm" || cmd == "yarn" || cmd == "cargo") && len(tokens) > 1 && tokens[1] == "test" ||
			(cmd == "python" || cmd == "python3" || cmd == "py") && len(tokens) > 2 && tokens[1] == "-m" && tokens[2] == "pytest" {
			targets = append(targets, strings.Join(tokens, " "))
		}
	}
	return strings.Join(targets, " && ")
}

func shellWords(command string) []string {
	var words []string
	var word strings.Builder
	var quote rune
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, r := range command {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
		} else if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			flush()
		} else {
			word.WriteRune(r)
		}
	}
	flush()
	return words
}
