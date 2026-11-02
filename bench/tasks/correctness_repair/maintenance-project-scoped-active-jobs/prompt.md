# Background maintenance runner is blocked by active or corrupt job files from other repositories

## Problem

In `go/internal/maintenance/runner.go`, `Runner.Run` checks `<state>/codex_exec_mcp/jobs/*.json` for active user jobs before starting a maintenance job and while polling a running maintenance job. Currently, `activeUserJobs` inspects every `.json` file in that shared directory without checking whether the job belongs to the target repository (`root`).

As a result:
1. A `"QUEUED"` or `"RUNNING"` job belonging to a different repository (`repoRoot` or `cwd` pointing to another project) causes `Runner.Run` to skip or cancel maintenance for the current repository.
2. An unreadable job file, an unattributable corrupt JSON file, or a job file whose `repoRoot`/`cwd` does not match the current repository causes `activeUserJobs` to report unknown job state and fail with `"active or unknown user job state; maintenance skipped"`.

## Expected Behavior

- Scope active user job detection in `go/internal/maintenance/runner.go` to the target repository (`root`).
- Identify a job record's project from `repoRoot` (falling back to `cwd` when `repoRoot` is empty) and compare it to `root` after resolving absolute paths, evaluating symlinks when possible, and cleaning paths (comparing case-insensitively on Windows and macOS).
- Ignore job files that cannot be read or whose project path is empty or refers to a different repository (including corrupt JSON files that do not identify `root` as their project).
- For job records that belong to `root`, continue treating corrupt JSON or a missing/non-string `state` as unknown state (`false, false`), and continue treating `"QUEUED"` or `"RUNNING"` (when not matching the excluded maintenance job ID) as an active user job (`true, true`).
