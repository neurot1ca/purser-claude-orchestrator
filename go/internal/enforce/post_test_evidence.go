package enforce

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"purser-hook/internal/fsretry"
	"purser-hook/internal/testevidence"
)

type hostTestRun struct {
	index int
	value map[string]any
}

func postReadTestEvidence(threadID string) ([]any, bool) {
	path := postCodexSessionPath(threadID)
	if path == "" {
		return nil, false
	}
	var data []byte
	if err := fsretry.Retry(func() (err error) {
		data, err = os.ReadFile(path)
		return err
	}, 2*time.Second); err != nil {
		return nil, false
	}
	latest := make(map[string]hostTestRun)
	lastEdit := -1
	for i, line := range strings.Split(string(data), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil || event["type"] != "event_msg" {
			continue
		}
		payload := mapValue(event["payload"])
		if payload["type"] != "item_completed" {
			continue
		}
		item := mapValue(payload["item"])
		switch item["type"] {
		case "file_change":
			lastEdit = i
		case "command_execution", "CommandExecution":
			command := postTestCommand(item)
			target, code := testevidence.ExitCode(command, item["exit_code"])
			if target == "" {
				continue
			}
			latest[target] = hostTestRun{index: i, value: map[string]any{"command": command, "exit_code": code}}
		}
	}
	if len(latest) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return latest[keys[i]].index < latest[keys[j]].index })
	runs := make([]map[string]any, 0, len(keys))
	failed := false
	for _, key := range keys {
		run := latest[key]
		if run.index < lastEdit {
			continue
		}
		runs = append(runs, run.value)
		if code, ok := run.value["exit_code"].(int); ok && code != 0 {
			failed = true
		}
	}
	runs = testevidence.Limit(runs)
	evidence := make([]any, len(runs))
	for i, run := range runs {
		evidence[i] = run
	}
	return evidence, failed
}

func postTestCommand(item map[string]any) string {
	if command, ok := item["command"].(string); ok {
		return command
	}
	argv, ok := item["command"].([]any)
	if !ok {
		return postParsedCommand(item["parsed_cmd"])
	}
	args := make([]string, 0, len(argv))
	for _, arg := range argv {
		value, ok := arg.(string)
		if !ok {
			return postParsedCommand(item["parsed_cmd"])
		}
		args = append(args, value)
	}
	if len(args) == 0 {
		return postParsedCommand(item["parsed_cmd"])
	}
	shell := strings.ToLower(filepath.Base(strings.ReplaceAll(args[0], `\`, `/`)))
	shell = strings.TrimSuffix(shell, ".exe")
	for i, arg := range args[1:] {
		flag := strings.ToLower(arg)
		isCommand := shell == "pwsh" || shell == "powershell"
		isCommand = isCommand && (flag == "-command" || flag == "-c")
		isCommand = isCommand || shell == "cmd" && flag == "/c"
		isCommand = isCommand || (shell == "bash" || shell == "zsh" || shell == "sh") && (flag == "-c" || flag == "-lc")
		if isCommand && i+2 < len(args) {
			return strings.Join(args[i+2:], " ")
		}
	}
	return strings.Join(args, " ")
}

func postParsedCommand(raw any) string {
	parsed, _ := raw.([]any)
	if len(parsed) != 1 {
		return ""
	}
	return stringValue(mapValue(parsed[0])["cmd"])
}

func postCodexSessionPath(threadID string) string {
	if threadID == "" || strings.ContainsAny(threadID, `/\\*?[]`) {
		return ""
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(userHome, ".codex")
	}
	now := time.Now()
	for _, day := range []time.Time{now, now.AddDate(0, 0, -1)} {
		folder := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
		paths, _ := filepath.Glob(filepath.Join(folder, "rollout-*-"+threadID+".jsonl"))
		if len(paths) > 0 {
			return paths[0]
		}
	}
	for _, year := range []int{now.Year(), now.Year() - 1} {
		pattern := filepath.Join(home, "sessions", strconv.Itoa(year), "[0-1][0-9]", "[0-3][0-9]", "rollout-*-"+threadID+".jsonl")
		paths, _ := filepath.Glob(pattern)
		if len(paths) > 0 {
			return paths[0]
		}
	}
	return ""
}
