# Report the first installed default reviewer agent when no reviewer is configured

## Problem

In `go/internal/initsetup/reviewer.go`, `reviewerCurrent` (called by `ReviewerRun` for the `list` action) currently assumes `"agy"` whenever the configuration file does not exist or no reviewer role is set, even when `agy` is not installed on the machine. When no reviewer is explicitly configured, listing the current reviewer should report the first installed agent that would actually be used by default, or `"none"` if no supported agent is installed.

## Required Behavior

Update `reviewerCurrent` in `go/internal/initsetup/reviewer.go` to accept the detected environment map (`reviewerCurrent(path string, detected map[string]any) (map[string]any, error)`) and pass `detected` from `ReviewerRun`:

1. **Missing config file or unconfigured reviewer role:**
   - When `path` does not exist (`os.IsNotExist(err)`), or when the parsed TOML config has neither `[reviewer].selected` nor an explicit reviewer agent in `[roles].reviewer` or `[coding].reviewer1_agent` (so `role == ""`), resolve the default reviewer from `detected["agents"]` (preserving any `reviewer1_model_requested` and `reviewer1_effort` read from `[coding]`).
   - Check agents in priority order `["agy", "claude", "codex"]`. The first agent whose entry in `detected["agents"]` has `"available": true` is selected:
     - `"agy"`: return `map[string]any{"agent": "agy", "model": first(configuredModel, agyReviewerModel()), "effort": nil}`.
     - `"claude"`: if `configuredEffort` is not one of `"low"`, `"medium"`, `"high"`, `"xhigh"`, `"max"`, set it to `"medium"`, and return `map[string]any{"agent": "claude", "model": "claude-haiku-5-5", "effort": configuredEffort}`.
     - `"codex"`: return `map[string]any{"agent": "codex", "model": "gpt-6-luna", "effort": "low"}`.
   - If none of `"agy"`, `"claude"`, or `"codex"` has `"available": true`, return `map[string]any{"agent": "none"}`.

2. **Explicit reviewer configurations remain unchanged:**
   - If `[reviewer].selected` is a table in the config file, return its `"agent"`, nullable `"model"`, and nullable `"effort"` regardless of `[roles].reviewer` or `detected["agents"]` availability.
   - If the resolved role is `"none"` (e.g. `[roles].reviewer = "none"` without `[reviewer].selected`), return `map[string]any{"agent": "none"}`.
   - If an explicit role (`[roles].reviewer` or `[coding].reviewer1_agent`) is set without `[reviewer].selected`, return that agent along with its nullable model and effort.
