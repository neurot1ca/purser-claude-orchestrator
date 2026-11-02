# Sweep leftover staging and backup directories during marketplace export instead of failing

## Problem

In `go/internal/release/release.go`, `ExportMarketplace(root, commit, marketplace string) error` constructs fixed PID-based paths in `marketplace`:
- `.claude-plugin-stage-<pid>`
- `.purser-previous-<pid>`
- `.claude-plugin-previous-<pid>`

Before creating the manifest staging directory, `ExportMarketplace` checks whether any of those paths already exist and immediately fails with `"stale marketplace export staging path exists"`. If a previous export failed, crashed, or left behind `.purser-previous-*`, `.claude-plugin-previous-*`, or `.claude-plugin-stage-*` directories (including from the same PID or an earlier run), subsequent exports fail or leave stale backup directories behind.

## Expected Behavior

In `ExportMarketplace` (`go/internal/release/release.go`):
- After ensuring `marketplace` exists, sweep any existing leftover entries in `marketplace` whose names start with `.purser-previous-`, `.claude-plugin-previous-`, or `.claude-plugin-stage-` by removing them (`os.RemoveAll`).
- Create the manifest staging directory with `os.MkdirTemp(marketplace, ".claude-plugin-stage-")` instead of a fixed PID-based path, and derive the backup directory suffix from the unique `purser-export-*` stage directory suffix (`.purser-previous-<stageID>` and `.claude-plugin-previous-<stageID>`), removing the pre-check that errors on `"stale marketplace export staging path exists"`.
