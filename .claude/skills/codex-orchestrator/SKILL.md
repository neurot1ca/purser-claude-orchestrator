---
name: codex-orchestrator
description: Use when you are about to delegate a repository code change to Codex and need to route, dispatch, and review it.
---

# Codex Orchestrator

`AGENTS.md`, `state/current.yaml`, and the active spec define authority. This skill supplies judgment; `.claude/hooks/orchestrator/` supplies mechanics. Delegation cannot create authority.

## Protocol

Write this as the exact first prompt line. The hook strips it, resolves model and effort, and rejects malformed or inconsistent calls:

```text
ORCH|task=<stable-id>|phase=<implementation|operation|plan>|route=<mechanical|subtle|normal|consequential|escalated|operation|research|plan_standard>[|reason=<new_task|continue|route_change|reanchor|repeated_failure|user_override|fix>][|review_findings=<F<16-hex>-<4-digits>[,...]>][|basis=<brief-basis>][|requested=user][|scope=<repo-relative-path[,repo-relative-path...]>][|eta=<N>m]
```

`task`, `phase`, and `route` are required; other fields are optional except: `basis` is required for `normal`, `consequential`, and `escalated`; `scope` is required for `phase=implementation`, omitted for `operation`, and optional for `plan`. Implementation scope must list bounded repo-relative paths, never the whole repository. `phase=plan` pairs only with `route=plan_standard`; it is read-only. `eta` is an integer from 1 through 600, without a leading zero, and sits beside the recorded actual duration. Use `review_findings` only with `reason=fix`, with 1–20 unique `F` + 16 hexadecimal digits + hyphen + four digits.

## Choose a route

Classify before dispatch. Choose the lowest route likely to succeed in one pass; default to `subtle` when decisions are stated. Use `mechanical` for narrow mechanical or counting work; `subtle` for narrow work with resolved but delicate semantics; `normal` for ordinary implementation or unresolved design; `consequential` when consequential behavior still needs diagnosis or design; `escalated` for a concrete escalation after a material miss, disagreement, or repeated failure; `operation` only for an established runbook; `plan_standard` only for read-only planning. Use `basis=design:<choice>` for open design with `normal`, `consequential`, or `escalated`. For implementation starts, those routes require that basis or `requested=user`; otherwise the hook downgrades to `subtle`. Resolved consequential implementation may use `subtle` only with an accepted map and every invariant explicit; review it as consequential. See `/purser:plan` for plan calls.

## Dispatch, review, and apply

1. Fix the design, route, scope, authority, and acceptance criteria. Start one fresh `purser:dispatcher` agent for each implementation or operation job; pass it the exact worker prompt, including the ORCH line, and exact tool arguments. Keep dispatcher instructions outside that prompt.
2. Dispatch in the background. Wait for the terminal result when needed. Read the receipt and the bundle files it names; the receipt is authoritative for effective model, effort, revision, and validation evidence. Do not repeat the receipt in the user-facing result.
3. Independently review every completion claim, objection, or refusal. Reject fluent results that fail review. Send material defects, failed criteria, contract violations, or required validation gaps back to the original job's dispatcher for repair; include valid objections in the repair instruction. Review the new delta after repair.
4. Main Claude owns routing, semantic review, disagreement resolution, acceptance, and commits. After review, call `codex_apply` with `jobId` and `revision` equal to the receipt's `revision` (`changed_paths_sha256`) to accept and apply in one call.
5. When the user approves multi-step work, record each step in your task list and keep it current; end the turn only for a real decision.

Read [route and prompt details](references/route_and_prompt.md) when model defaults, worker prompt construction, or dispatch mechanics matter, including the research route. Read [async plans and recovery](references/async_plans_and_recovery.md) when work has milestones, overlapping scopes, or apply conflicts. Read [thread and repair limits](references/thread_and_repair.md) when continuing, reanchoring, failing over, or deciding when to stop. Read [review and acceptance details](references/review_and_acceptance.md) for audits, evidence, validation, or residual risk. Read [session conventions](references/session_conventions.md) for language, reply format, or gear requests. Read [conditional delegation](references/conditional_delegation.md) for unverified premises, conflicting criteria, optional lanes, deep exploration, or UI/web/greenfield work. Read [conditional review](references/conditional_review.md) for causal claims or residual risk. Read [receipt examples](references/receipt_examples.md) when showing receipt lines.
