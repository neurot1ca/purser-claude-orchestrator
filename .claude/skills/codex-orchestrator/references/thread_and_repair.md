# Thread and repair limits

Use `codex-reply` for follow-up or repair while the worker, diagnosis, task identity, route, model, and effort still hold. Start fresh for failover, stale diagnosis, material P0/P1 error with plausible anchoring, repeated repair failure, or justified model/effort change; never merely to simulate independence. Use a new task ID for new work after a recorded final outcome. For an intentional fresh start on a compatible unfinished task, set `reason=reanchor`.

A compatible recorded `codex-reply` may inherit the ORCH line.

For authorized bounded work, allow the initial implementation, at most two ordinary material repairs, then at most one justified escalated repair. Stop earlier when criteria are met, a human decision or material scope expansion is needed, authority is ambiguous, infrastructure blocks progress, or repairs cease material progress. Do not loop for style, optional optimization, speculative cleanup, or confidence without concrete risk.

A repair belongs to its original job's dispatcher; preserve the task and thread identity and the existing repair limits. The dispatcher starts the repair on that same thread and waits, then returns terminal identity and state. Main Claude collects the repair bundle once with `codex_result`, reviews the new delta, and applies the reviewed revision directly. A dispatcher apply is not part of either lifecycle.
