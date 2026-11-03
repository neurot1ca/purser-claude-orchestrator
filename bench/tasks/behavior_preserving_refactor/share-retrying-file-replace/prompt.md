# Share bounded retry behavior for atomic file replacement

## Problem

Atomic file writes across context, job, and enforcement state use separate rename paths. Some writes retry transient filesystem locks, while others fail immediately, and the retry policies differ. This can make otherwise successful state updates fail when Windows or another filesystem scanner briefly holds a target file.

## Requirements

Consolidate atomic replacement behavior across the affected Go packages while preserving each write's existing temp-file, data, and error-handling behavior. Retry transient replacement failures for a bounded period with a short backoff, including permission and busy/sharing errors supported on each platform. Return immediately for non-transient errors and return the last replacement error if retries are exhausted. Preserve existing package test seams where callers already provide a replace operation.

## Interface

- In `go/internal/fsretry/replace.go`, define a function with declaration prefix `func ReplaceWith(`.
- In `go/internal/contextio/context.go`, call `fsretry.Replace(`.
- In `go/internal/jobs/store.go`, call `fsretry.ReplaceWith(` and remove `func transientReplace(`.
- In `go/internal/enforce/observations.go`, call `fsretry.ReplaceWith(`.

## Test seam compatibility

The trusted checks use the package test seams `now` and `sleep` in `go/internal/fsretry/replace_test.go`. Preserve or provide those package-level seams so the trusted checks compile; they let tests control elapsed time and backoff without waiting.
