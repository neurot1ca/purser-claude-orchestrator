# Background maintenance runner is blocked by active or corrupt job files from other repositories

## Problem

In `go/internal/maintenance/runner.go`, `Runner.Run` checks `<state>/codex_exec_mcp/jobs/*.json` for active user jobs before starting a maintenance job and while polling a running maintenance job. Currently, `activeUserJobs` inspects every `.json` file in that shared directory without checking whether the job belongs to the target repository (`root`).

As a result:
1. A `"QUEUED"` or `"RUNNING"` job belonging to a different repository (`repoRoot` or `cwd` pointing to another project) causes `Runner.Run` to skip or cancel maintenance for the current repository.
2. Job files that cannot be read, cannot be attributed to a project, or belong to a different repository can interfere with maintenance for the current repository.

## Expected Behavior

- Scope active user job detection in `go/internal/maintenance/runner.go` to the target repository (`root`).
- Identify a job record's project from `repoRoot` (falling back to `cwd` when `repoRoot` is empty) and compare it to `root` after resolving absolute paths, evaluating symlinks when possible, and cleaning paths (comparing case-insensitively on Windows and macOS).
- Ignore job files that cannot be read or whose project path is empty or refers to a different repository, including syntactically corrupt files that cannot be attributed to the current repository.
- For readable, valid job records attributed to `root`, treat a missing or non-string `state` as unknown state (`false, false`). Treat `"QUEUED"` or `"RUNNING"` as an active user job (`true, true`) unless its job ID matches the excluded maintenance job ID.
