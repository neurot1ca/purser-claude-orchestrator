# Purser A/B Benchmark Contract

This directory defines the paired A/B benchmark contract comparing Claude Code with Purser enabled (`on`) versus disabled (`off`) on a fixed coding suite. Machine-readable parameters live in [`protocol.json`](protocol.json).

## 1. Confirmatory Endpoints and Descriptive Metrics

The benchmark evaluates **two confirmatory endpoints only**:

1. **Main-Claude tokens (`main_claude_tokens`):** the unweighted sum of unique main-session `input_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`, and `output_tokens`.
2. **Wall time to a verified success (`verified_wall_time`):** elapsed time from process launch through delivered changes, process/job quiescence, and authoritative verification for passing runs; unsuccessful runs are charged the task's predeclared deadline.

### Descriptive-Only Quality and Timing

- **Pass rates are descriptive only:** report passed/attempted counts and percentages per arm alongside paired outcome counts and failure reasons.
- There is **no quality non-inferiority test** and **no 80-task requirement**.
- Do not claim savings at "equal quality", "preserved quality", or non-inferior quality. Every savings statement must be accompanied by the observed pass counts and the deadline-penalty convention for failed runs.
- Observed elapsed time (without deadline substitution) is reported descriptively alongside the confirmatory wall-time endpoint.

### Separate Worker Accounting

- Delegated worker usage is collected across all turns, retries, reviews, and repairs and reported separately by provider, model, and role.
- Provider-specific sub-counters (such as cached-input or reasoning subsets) retain their provider semantics and are never combined with main-Claude tokens into a net token figure.
- Missing or incomplete worker usage is recorded as `"unknown"`, never `0`.
- Missing, malformed, or unreconciled main-Claude transcript accounting blocks token claims.

## 2. Statistical Inference

Tasks—not individual repetitions—are the independent units of analysis.

For endpoint $m$, task $i \in \{1, \dots, K\}$, and paired repetition $r \in \{1, \dots, R\}$:

$$d_{ir,m} = \log\!\left(\frac{Y_{\mathrm{on},ir,m}}{Y_{\mathrm{off},ir,m}}\right), \qquad \bar{d}_{i,m} = \frac{1}{R}\sum_{r=1}^{R} d_{ir,m}$$

Across the $K$ confirmation tasks, compute the sample mean $\overline{d}_m$ and task-level sample standard deviation $s_m$ of $\{\bar{d}_{i,m}\}_{i=1}^{K}$. Each endpoint uses a **one-sided upper confidence bound at $\alpha = 0.025$** (Bonferroni correction across the two confirmatory claims, controlling familywise Type I error at $0.05$):

$$U_m = \overline{d}_m + t_{0.975,\,K-1}\frac{s_m}{\sqrt{K}}, \qquad L_m = 1 - \exp(U_m)$$

A confirmatory savings claim on endpoint $m$ requires $L_m > 0$. Failed runs remain in both endpoint calculations and pass-rate denominators; never restrict analysis to jointly passing pairs.

## 3. Task Bank, Calibration, and $(K, R)$ Selection

Before calibration or confirmation, run `node bench/run.mjs validate` and require every task reference to pass. `validate --tasks <glob>` checks a selected subset without starting Claude or downloading the pinned engine. For Go checks, validation also compiles trusted check files over a clean `start_commit` checkout without the reference patch and requires the prompt to name identifiers needed by undefined-symbol or call-signature diagnostics. Unrecognized compile failures and timeouts fail with the compiler output tail; non-Go checks are skipped with a note.

### Four Task Classes and Initial 20-Task Bank

Tasks are balanced across four coding classes:

1. `correctness_repair` — fix an existing bug verified by reproducer and regression checks.
2. `feature_extension` — add behavior across parsing, validation, and execution.
3. `behavior_preserving_refactor` — consolidate duplicated logic while preserving behavioral contracts.
4. `failure_path_repair` — fix cancellation, recovery, or validation on failure paths.

The initial bank comprises **20 distinct tasks**:

- **Calibration set:** 8 tasks (2 per class) $\times$ 2 paired repetitions = 16 pairs (32 arm runs). **Calibration is excluded from confirmation.**
- **Initial held-out confirmation set:** 12 tasks (3 per class).

Authoritative grading runs outside the agent workspace by applying the submitted production diff to a clean verification snapshot so tests or checks cannot be weakened by the agent.

### Prospective Power Targets and Allocation Formula

Power targets are fixed before calibration:

- **Main-Claude token reduction target:** $\delta_T = 40\%$ ($0.40$).
- **Verified wall-time reduction target:** $\delta_W = 30\%$ ($0.30$).
- **Power:** $90\%$ per endpoint ($\ge 80\%$ joint power by the union bound).

From calibration, estimate between-task variance $\tau_m^2$ and within-task paired log-ratio variance $\sigma_m^2$. The squared standard error of the task-mean log ratio is:

$$SE_m^2 \approx \frac{\tau_m^2 + \sigma_m^2 / R}{K}$$

Choose integer $R \ge 1$ and confirmation task count $K$ (constrained to be a **multiple of 4** for class balance) satisfying for both endpoints:

$$K \ge \frac{\left(t_{0.975,\,K-1} + z_{0.90}\right)^2 \left(\tau_m^2 + \sigma_m^2 / R\right)}{\left|\log(1 - \delta_m)\right|^2}$$

### Hard Floor $K_{\min} = 12$

Confirmation requires at least **$K_{\min} = 12$ tasks** (3 per class). Under approximately normal independent task effects, the relative standard error of the sample variance is:

$$\sqrt{\frac{2}{K - 1}}$$

Requiring $\sqrt{2 / (K - 1)} \le 50\%$ and rounding up to a balanced multiple of 4 yields $K_{\min} = 12$ ($\approx 43.3\%$, whereas $K = 8$ gives $\approx 53.5\%$).

**Explicit limitation:** $K_{\min} = 12$ is a variance-estimate stability criterion only; **this is not a validity guarantee** for cluster or small-sample t inference. Task independence, skewness, outliers, and interval coverage must still be validated before publishing inferential claims. The confirmation task set and schedule must be frozen before confirmation runs begin.

## 4. Immutable Execution and Treatment Isolation

All runtime state and raw artifacts are written under git-ignored `.tmp/bench/<campaign>/`. Local raw output inside `bench/` is ignored via [`bench/.gitignore`](.gitignore).

Each campaign provisions the pinned Purser engine from the versioned GitHub release ZIP, checks its SHA-256 against `protocol.json`, and verifies the plugin version and host executable before use.

Each campaign provisions:

- A separate engine directory pinned to Purser `v0.1.228` (commit `10fa36db7bbaf5f0f5a90c4c02eb46a7bfa890c2`), never the active working tree.
- A verified release archive containing the host-matched engine binary.
- Separate per-arm target worktrees, settings files, `PURSER_STATE_DIR`, and transcript directories.

Both arms launch via `claude -p` (verified against Claude CLI `2.1.317`) with identical arguments and stdin prompt bytes:

```text
claude -p \
  --model <exact-model-id> \
  --effort <fixed-effort> \
  --session-id <fresh-uuid> \
  --setting-sources project,local \
  --settings <arm-settings.json> \
  --plugin-dir <pinned-engine> \
  --output-format stream-json \
  --verbose \
  --include-hook-events
```

Both arm settings use `permissions.defaultMode: "bypassPermissions"`; the sole treatment difference is `enabledPlugins["purser@inline"]` (`true` for `on`, `false` for `off`). Do not pass `--bare` or `--safe-mode`, as those alter baseline tools, hooks, and session behavior. Paired runs balance `on`-first and `off`-first execution order using a recorded randomization seed.

Claude uses the user's default config directory for authentication while `project,local` sources exclude user settings; enabled non-inline Purser plugins from the user settings are explicitly disabled in both arm settings files. Parent Claude Code session variables are removed from the child environment, while provider variables and the per-run Purser state directory are preserved.

Main assistant usage snapshots with repeated message IDs are counted once using the maximum observed value per category; the final `result` usage supplies authoritative main-session output tokens because assistant snapshots can report partial output. Input and cache categories remain reconciled against the result usage, and worker accounting remains separate.

## 5. Cross-OS Requirements (Windows, macOS, Linux)

The runner and analyzer must obey the following cross-platform rules across Windows, macOS, and Linux:

- **Portable paths and subprocess execution:** use `node:path` for all path manipulation and spawn processes with explicit argument arrays and `shell: false`.
- **Windows `.cmd` shim resolution:** resolve the underlying executable entrypoint rather than invoking `.cmd` batch shims through a shell.
- **Hidden Windows child processes and process-tree cleanup:** pass `windowsHide: true` on Windows, drain `stdout`/`stderr` continuously, perform ownership-aware process-tree termination on completion or deadline expiry, and verify no orphaned worker processes remain.
- **Transcript file-lock resilience:** use bounded close/reopen retry with backoff when reading transcripts or state files that encounter Windows sharing violations (`EBUSY`/`EPERM`/`EACCES`) or antivirus scan locks, finalizing only after writer processes exit.
- **Per-OS separate cohorts:** validate and report Windows, macOS, and Linux as separate cohorts; **never pool** runs across different operating systems into a combined estimate or bound.

## 6. Provenance Requirements

Every published or generated result record and table must carry these provenance fields:

- `engine_sha`
- `harness_sha`
- `suite_hash`
- `campaign_id`
- `n_tasks`
- `repetitions`
- `n_pairs`
- `n_on`
- `n_off`
