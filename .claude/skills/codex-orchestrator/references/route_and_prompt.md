# Route and prompt details

Delegate bounded, verifiable work with acceptance criteria, regardless of artifact type: implementation, repair, analysis, extraction, investigation, schema work, migration, or scoped API probe. Turn repeated checks or reusable evidence into written, tested instruments, not ad-hoc commands. Keep framing, answer criteria, authority, routing, review, and disagreement with the orchestrator. Do bookkeeping directly when delegation costs more.

## Protocol examples

Use a new task ID for new work after a recorded final outcome. For an intentional fresh start on a compatible unfinished task, use `reason=reanchor`. Example repair reference: `reason=fix|review_findings=F0123456789abcdef-0001`. `review_findings` contains 1–20 unique references matching `F` plus 16 hexadecimal digits, a hyphen, and four digits. Example duration: `eta=15m`.

## Route selection

| Task class | Route | Default Codex model and effort |
|---|---|---|
| Mechanical, narrow, low-risk; status or counting | `mechanical` | `gpt-6-luna` + `high` |
| Narrow but semantically subtle, or fully resolved implementation | `subtle` | `gpt-6-luna` + `high` |
| Ordinary implementation, debugging, or multi-file work with design left | `normal` | `gpt-6.1-sol` + `medium` |
| Consequential and unresolved; diagnosis, design, or audit must find the rule | `consequential` | `gpt-6.1-sol` + `high` |
| Concrete escalation after a material miss, disagreement, or repeated failure | `escalated` | `gpt-6.1-sol` + `xhigh` |
| Established runbook operation: deploy, runner control, status, fetch, packaging | `operation` | `gpt-6-luna` + `high` |

The receipt reports the effective model and effort; follow it when they differ from defaults. With Claude as coder, the routes use Haiku (mechanical, operation), Sonnet medium (subtle, normal), and Opus high (consequential, plan) or xhigh (escalated), always the newest model of each tier. Research and study jobs use `route=normal` only when the question or method remains open, with `basis=design:<choice>`. `gpt-6-luna` + `xhigh` is invalid; unresolved work uses `gpt-6.1-sol`.

Consequential domains include concurrency, timing and clocks, state machines, economics and execution, auth, durability and recovery, causal evidence, fail-closed behavior, live/risk gates, and subtle cross-module invariants. A domain label alone does not decide whether design is resolved. An accepted map must name files, symbols, invariants, failure direction, and behavior, leaving no semantic choice to the worker. Avoid cheap-first cascades: repair costs review. Effort cannot fix contradictory or unresolved prompts.

## Dispatch mechanics

Every implementation and operation job runs through a `purser:dispatcher` agent. Start one fresh dispatcher (a new Agent call) per job, after main Claude fixes design, route, scope, authority, and acceptance criteria. Pass the exact worker prompt, including its ORCH line, and exact tool arguments. Keep dispatcher instructions outside the worker prompt.

Name background work for the user: the Agent description is the job label in the user's language; a host test run's description is `Tests: <label>` in that language; each shell or PowerShell call Claude runs in the background gets a short description in that language, never the raw command or a job ID. Tell the dispatcher `User language: <language>` so its waiter description uses that language. The dispatcher runs in the background; keep talking with the user meanwhile.

Runtime configuration owns capability; never pass `sandbox` or `approval-policy`. Fail over on typed quota or credit exhaustion, never route proactively by quota. Pass `label` on every `codex_start`, `codex`, and `codex-reply` call: a short human description in the user's language, at most 80 characters. A reply may omit it only when the existing thread has a saved label. Purser emits the dispatch receipt; Claude must not write or repeat one. Explain the result in plain text.

## Worker prompt

For implementation and plan calls, the pre-hook adds exactly one `CODE_MAP=<path> STATUS=<FRESH|STALE|UNAVAILABLE>` line. Do not add it by hand. Bounded refresh reuses a fresh map and leaves a stale map when refresh is not quick; map generation failure never blocks dispatch. The map is navigation only: workers verify current source and open relevant sections on demand. Operation calls do not receive a code map.

Write worker prompts with an `Objective:` line and an `Acceptance criteria:` list. Missing labels force FULL review because the hook cannot ground the contract. Keep the ORCH line and bounded scope.

- Give verified task context, resolved behavior, invariants, failure direction, and only necessary exact examples or schemas.
- Rely on injected `AGENTS.md` for stable repo rules; state only task-specific boundaries and uncertain authority.
- Name the smallest authoritative source set and affected boundary; let Codex inspect relevant callers, tests, and contracts.
- Omit procedural implementation or read lists unless order or exact format is contractual; exclude speculative redesign, cleanup, and polish.
- Give one completion condition covering deliverable, scoped verification, and residual gaps; keep `VALIDATION:` where required.
- Never shorten exact labels, literal output schemas, required validation, or task-specific safety gates.
- For execution tasks, do not request an upfront plan, preamble, or status narration; ask for a plan only when the plan is the deliverable.

State the failure class as the requirement. Concrete failures are non-exhaustive tests unless the domain genuinely requires a finite table; then require the residual gap to be reported.
