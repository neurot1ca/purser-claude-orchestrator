# Share Codex Go cache setup across host runs

## Problem

Codex host launches configure Go's build cache and temporary build directory inline with the worktree-specific launch setup. That couples cache configuration to whether a worktree exists, and makes the sandbox writable-root configuration easy to overwrite when combining the worktree with cache directories or existing user-provided roots.

## Requirements

Consolidate the Codex Go cache and writable-root setup so it can be applied consistently to host launches with and without a worktree. Preserve these behaviors:

- When `GOCACHE` or `GOTMPDIR` is already set in the process environment, do not replace that setting or add its default directory.
- For unset variables, use the state-root cache directories `cache/go-build` and `cache/go-tmp`, create them with restrictive permissions, and add them to the child environment and sandbox writable roots.
- Include a worktree in writable roots only when one exists, and preserve existing `sandbox_workspace_write.writable_roots` entries when combining configuration.
- Preserve unrelated configuration, remove conflicting dotted `sandbox_workspace_write.*` entries when consolidating the object, and avoid installing an empty writable-roots override.
- If creating a required cache directory fails, fail the Codex host launch through its existing error path.

## Interface

- In `go/internal/jobs/host.go`, define a function with declaration prefix `func configureCodexGoCache(` and include the call `configureCodexGoCache(&args, &childEnv, stateRoot, writableRoots)`.
