# Preserve prior thread identity on replay preparation failure and reflect terminal index failure on the job board

## Problem

Two failure-path issues occur when an asynchronous job fails during replay preparation or result finalization in `go/internal/enforce`:

1. **Prior thread identity is lost on replay preparation failure (`go/internal/enforce/failure_shared.go`):**
   When `saveFailureSession` records an event with `error_code == "JOB_REPLAY_PREPARATION_FAILED"`, it unconditionally overwrites the thread ID from `pending["prior_thread_id"]` with `event["replay_thread_id"]`. If `event["replay_thread_id"]` is absent or empty, `threadID` becomes empty and the saved ledger record loses its `live_thread` metadata (`thread_id`, `task_id`, `route`, `model`, `effort`), preventing compatible follow-up retries on that thread.

2. **Finalization failures after job completion still render as succeeded on the job board (`go/internal/enforce/post_board.go`):**
   Result finalization is tracked in the async index entry (`item.index`) and can fail after the underlying job record (`item.job`) has already reached `state: "SUCCEEDED"`. In `PostProjectJobBoard`, board row rendering inspects `j["state"]` directly without checking whether the async index recorded a terminal failure (`idx["stage"] == "FAILED"` or `idx["terminal_failure"] == true`). As a result, a job whose result finalization failed is rendered as `coded · fetching result` (or `done` when `applyState` is `superseded`/`rejected` or `applied`) instead of `failed`.

## Expected Behavior

- In `saveFailureSession`, when `event["error_code"] == "JOB_REPLAY_PREPARATION_FAILED"`, only override `pending["prior_thread_id"]` if `event["replay_thread_id"]` is a non-empty string; otherwise retain `pending["prior_thread_id"]`.
- In `PostProjectJobBoard`, treat the effective job state for board display as `"FAILED"` whenever the async index entry has `stage == "FAILED"` or `terminal_failure == true`, so that `"SUCCEEDED"` state transitions (`coded · fetching result`, `in review`, `accepted`, `done`) and `applyState` / `applied` `"done"` overrides do not mask the terminal failure.
