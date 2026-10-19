# Async plans and recovery

Treat plan paths as estimates. For an accepted plan, dispatch every entry in `ready_milestones` in the same turn, using one background `purser:dispatcher` Agent call per pass. Submit all calls before waiting for any result. Include shared generated files and validation inputs when judging disjointness. For worktree jobs, dispatch overlapping paths immediately: Purser queues hot-path conflicts as `QUEUED` and auto-starts them on lease release; otherwise jobs run in parallel worktrees and resolve overlap at apply. Wait only for a logical dependency, such as a milestone needing the previous milestone's accepted result; a reply on a conflicting scope is still denied. Review each result on its own delta; otherwise run disjoint work sequentially.

Use the SessionStart duration hint for `eta` when present. The dispatcher starts every job with `codex_start`; for continuation pass `mode=reply`, `threadId`, the same ORCH `task`, and no `cwd`. Use `codex_cancel` only at user request. Never cancel a job that was applied; cancelling discards it.

After `codex_apply` reports a replay conflict, main Claude resolves and commits the files by hand, then calls `codex_apply` with the same `jobId` and `resolved: true`. This records the hand-applied job as accepted and releases its scope lease. When main Claude applies an accepted job by hand for any other reason (stale apply, failed finalization), it calls `codex_apply` with that `jobId` after committing: the files already match the worktree, so the job is recorded as applied and its lease is released.

When an accepted part of an approved multi-part task finishes, start the next part in the same turn without asking. Stop only for approval-gated actions, real decisions, failed review, or residual risk the user must weigh. Quota is not a reason to stop unless 5h or 7d remaining is at most 10%; then warn before the task and let the user choose.

With several jobs, keep a list of finished results the user has not yet decided on. Present them one at a time in finish order, and do not move to the next one or to new work until the user has answered on the current one.
