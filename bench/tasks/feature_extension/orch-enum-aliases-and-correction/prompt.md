# Expand ORCH header enum aliases and ensure suggested correction lines are valid

## Problem

`ParseOrchPrompt` in `go/internal/policy/orch.go` currently normalizes only a small set of `phase` and `reason` aliases and does not normalize `route` aliases at all. In addition, when an `ORCH|...` header line fails validation, `correctedOrchLine` can suggest a replacement header that still fails to parse and lacks valid enum placeholders, or can suggest overly loose edit-distance matches for short or distant strings.

## Required Behavior

1. **Enum alias normalization in `ParseOrchPrompt` and `normalizeOrchEnum`:**
   - Apply `normalizeOrchEnum` to `route` as well as `phase` and `reason` during `ParseOrchPrompt` (recording `<key>=<old>-><new>` in `Normalizations` when changed).
   - After lowercasing and replacing `-` with `_`, map the following aliases to their canonical values:
     - `phase`: `implement`, `impl`, `code`, `coding` -> `implementation`; `op`, `ops` -> `operation`; `planning` -> `plan`.
     - `reason`: `feedback`, `review_feedback`, `review`, `review_fix`, `sendback`, `send_back`, `repair`, `bugfix`, `bug_fix`, `rework` -> `fix`; `new` -> `new_task`; `cont`, `resume` -> `continue`; `reroute`, `re_route` -> `route_change`; `re_anchor` -> `reanchor`; `override` -> `user_override`.
     - `route`: `mech` -> `mechanical`; `op`, `ops` -> `operation`; `plan` -> `plan_standard`; `escalate` -> `escalated`.
   - Any canonical value for `phase` (`implementation`, `operation`, `plan`), `reason` (`new_task`, `continue`, `fix`, `route_change`, `reanchor`, `repeated_failure`, `user_override`), `route` (`mechanical`, `subtle`, `normal`, `consequential`, `escalated`, `operation`, `plan_standard`, `research`), or `requested` (`user`) should also normalize case and hyphens to the canonical value.

2. **Closest enum matching and placeholders (`closestOrchEnum` and `correctedOrchLine`):**
   - For single-candidate enums (`requested`), `closestOrchEnum` should return that single candidate (`user`).
   - If `normalizeOrchEnum(key, value)` matches a canonical candidate, `closestOrchEnum` should return it directly.
   - Otherwise, only accept a fuzzy edit-distance match when `distance <= 3`, `distance <= len(best)/2`, `distance <= len(clean)/2`, and there is no tie (`clean` being the lowercased, underscore-normalized input).
   - In `correctedOrchLine`, when `closestOrchEnum(key, value)` returns empty for an enum key (`phase`, `reason`, `route`, `requested`) that has candidates, set `corrected[key]` to `"<" + strings.Join(candidates, "|") + ">"` (for example, `phase=<implementation|operation|plan>`).

3. **Cross-field repairs in `correctedOrchLine`:**
   - If `review_findings` is present in `corrected`, set `corrected["reason"] = "fix"`.
   - If `corrected["route"] == "plan_standard"` and `corrected["phase"] != "plan"`, set `corrected["phase"] = "plan"`; else if `corrected["phase"] == "plan"` and `corrected["route"] != "plan_standard"`, set `corrected["route"] = "plan_standard"`.
   - If `corrected["route"] == "research"` and `corrected["phase"] != "operation"`, set `corrected["phase"] = "operation"`.
   - If `corrected["phase"] == "implementation"` and `strings.TrimSpace(corrected["scope"]) == ""`, set `corrected["scope"] = "<paths>"`.
   - If `strings.TrimSpace(corrected["basis"]) == ""` and `corrected["route"]` is `normal`, `consequential`, or `escalated`, and `corrected["requested"] != "user"`, set `corrected["basis"] = "design:<...>"`.
