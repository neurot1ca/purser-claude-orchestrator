# Share bounded retry behavior for atomic file replacement

## Problem

Atomic file writes across context, job, and enforcement state use separate rename paths. Some writes retry transient filesystem locks, while others fail immediately, and the retry policies differ. This can make otherwise successful state updates fail when Windows or another filesystem scanner briefly holds a target file.

## Requirements

Consolidate atomic replacement behavior across the affected Go packages while preserving each write's existing temp-file, data, and error-handling behavior. Retry transient replacement failures for a bounded period with a short backoff, including permission and busy/sharing errors supported on each platform. Return immediately for non-transient errors and return the last replacement error if retries are exhausted. Preserve existing package test seams where callers already provide a replace operation.

Use a two-second retry window, with delays starting at 25 ms, doubling up to a 250 ms maximum, and clipping the last delay to the remaining window. After that last delay, attempt replacement once more before returning the last error. Treat wrapped `os.ErrPermission` and `syscall.EBUSY` as transient on all platforms, and Windows error numbers 5 and 32 as transient only on Windows (in particular, do not retry errno 5 on other platforms).

## Interface

- In `go/internal/fsretry/replace.go`, provide `func Replace(src, dst string) error` and `func ReplaceWith(replace func(string, string) error, src, dst string) error`; `Replace` uses the package's `rename` seam.
- In `go/internal/contextio/context.go`, call `fsretry.Replace(`.
- In `go/internal/jobs/store.go`, call `fsretry.ReplaceWith(` and remove `func transientReplace(`.
- In `go/internal/enforce/observations.go`, call `fsretry.ReplaceWith(`.

## Test seam compatibility

The trusted checks use the package test seams `now` (`func() time.Time`), `sleep` (`func(time.Duration)`), and `rename` (`func(string, string) error`) in `go/internal/fsretry/replace_test.go`. Preserve or provide those package-level seams so the trusted checks compile; `now` and `sleep` let tests control elapsed time and backoff without waiting, and `rename` supplies the replace operation that the package can substitute in tests.
