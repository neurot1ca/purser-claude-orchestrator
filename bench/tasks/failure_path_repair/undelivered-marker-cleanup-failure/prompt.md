# Report undelivered async marker removal errors and contention marker close failures

## Problem

In `go/internal/enforce`, file cleanup and close errors on two marker paths are silently ignored:

1. **Undelivered async start marker removal (`go/internal/enforce/pre.go`):**
   `cleanupUndeliveredAsyncStart(root, pendingPath, markerPath string) error` schedules `defer os.Remove(markerPath)` and discards the returned error. If removing `markerPath` fails with an error other than `os.ErrNotExist` (for example, when `markerPath` is a non-empty directory or cannot be unlinked) and the rest of the cleanup function returned `nil`, `cleanupUndeliveredAsyncStart` still returns `nil` instead of surfacing the marker removal error.

2. **Task usage contention marker file close (`go/internal/enforce/task_usage.go`):**
   `markTaskUsageContention(root, session, taskID string) string` opens the contention marker file with `defer file.Close()` and returns `"CREATED"` without checking whether `file.Close()` succeeded.

## Expected Behavior

- In `cleanupUndeliveredAsyncStart`, still remove `markerPath` on exit, and if `os.Remove(markerPath)` returns a non-nil error that is not `os.ErrNotExist` when the function's primary return error is `nil`, return that removal error.
- In `markTaskUsageContention`, close the marker file explicitly on write/sync failure paths and check the error from `file.Close()` before returning `"CREATED"`, returning `"FAILED"` if closing the file fails.
