# Review and acceptance details

For mutations, review the delta, then changed behavior and invariants, then material callers, callees, and boundaries. For read-only work, start with cited sources, method, and result. Expand evidence only for concrete uncertainty, failed checks, disagreement, dependency, or risk.

Reviewer is independent input, not acceptance. Claude fully reviews all consequential and escalated work. Request repair only for a material defect, failed criterion, contract violation, or required validation gap. After repair, review the new delta, blocking finding, and newly affected boundaries.

Workers run focused validation, never full suites. Post-apply host validation selects changed packages; manifest changes can select the full module. It may return pending, so collect the terminal result before using it as evidence. When `worker_validation.source` is `HOST_TEST_EVIDENCE` with status `PASS`, the host saw the worker's test runs pass: do not rerun those focused tests; rerun only when that evidence is missing, `UNKNOWN`, or `FAIL`. Before a commit or release, read the latest result and run any required full check its commands do not cover. Report a validation failure without named tests as a Purser gap. If no host validation is returned, run the project's full check once before the commit.

On `claude_review_mode: AUDIT`, use one Claude turn to check the bundle's task contract, reviewer result or eligible skip, worker validation, delta summary, and safety fields. Accept and commit only with sufficient evidence and commit authority; otherwise decline with the specific gap. Reopen raw diffs or shell review only for concrete inconsistency. Send Reviewer's required changes through the existing thread; do not edit worker code yourself.

Close every review with exactly one typed verdict and name the evidence that decides it:

- `VERIFIED` — the claimed effect was observed, not merely reported.
- `NOT VERIFIED` — the claim failed or the evidence contradicts it.
- `INCONCLUSIVE` — the available check could not decide; this is not acceptance.

Proceeding after `INCONCLUSIVE` is a user decision. Never round it up to success.
