package main

import (
	"fmt"
	"os"
	"strconv"

	"purser-hook/internal/jobs"
)

func waitMain(args []string) int {
	id, owner, maxSeconds, ok := parseWaitArgs(args)
	if !ok {
		fmt.Printf("purser-wait %s INVALID_ARGUMENT\n", id)
		return 2
	}
	if owner != "" {
		_ = os.Setenv("PURSER_SESSION_OWNER", owner)
	}
	ctx, cancel, err := sessionContext(os.Stderr)
	if err != nil {
		return 2
	}
	defer cancel()
	result := jobs.WaitContext(ctx, stateRoot(), id, maxSeconds)
	for _, line := range result.Lines {
		if _, err := fmt.Fprintln(os.Stdout, line); err != nil {
			return 1
		}
	}
	return result.ExitCode
}

func parseWaitArgs(args []string) (id, owner string, maxSeconds int64, ok bool) {
	id = "invalid"
	if len(args) == 0 {
		return id, "", 0, false
	}
	id = args[0]
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--session-owner":
			if owner != "" || i+1 >= len(args) {
				return id, "", 0, false
			}
			i++
			owner = args[i]
			if owner == "" {
				return id, "", 0, false
			}
		case "--max-seconds":
			if maxSeconds != 0 || i+1 >= len(args) {
				return id, "", 0, false
			}
			i++
			value, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil || value <= 0 {
				return id, "", 0, false
			}
			maxSeconds = value
		default:
			return id, "", 0, false
		}
	}
	return id, owner, maxSeconds, true
}

func waitValidationMain(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stdout, "purser-validation INVALID_ARGUMENT")
		return 2
	}
	result := jobs.WaitApplyValidation(args[0], args[1], args[2])
	for _, line := range result.Lines {
		if _, err := fmt.Fprintln(os.Stdout, line); err != nil {
			return 1
		}
	}
	return result.ExitCode
}
