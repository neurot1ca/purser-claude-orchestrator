# Retry transient file locks when saving ledger records and include the underlying error in failure results

## Problem

Saving session ledger records in `go/internal/ledger/ledger.go` and retrying file replacements in `go/internal/fsretry/replace.go` have two failure-handling gaps:

1. **Transient lock errors are not retried during ledger replacement:**
   - In `go/internal/fsretry/replace.go`, `transient(err error) bool` on Windows checks `syscall.Errno(5)` (`ERROR_ACCESS_DENIED`) and `syscall.Errno(32)` (`ERROR_SHARING_VIOLATION`), but omits `syscall.Errno(33)` (`ERROR_LOCK_VIOLATION`).
   - In `go/internal/ledger/ledger.go`, `Save(root string, record map[string]any) Result` calls `replaceFile(tempPath, target)` directly instead of routing through `fsretry.ReplaceWith(replaceFile, tempPath, target)`. As a result, a transient permission or sharing/lock error during atomic replacement immediately fails `Save`.

2. **Underlying write/replacement errors are discarded on `LEDGER_WRITE_FAILED`:**
   When `Save` fails to create the target directory, create the temporary file, write/sync/close the temporary file, or replace the target file, it returns `Result{Status: "UNKNOWN", Code: "LEDGER_WRITE_FAILED"}` with a `nil` `Record`, discarding both the error message and the root-cause error type.

## Expected Behavior

- In `go/internal/fsretry/replace.go`, treat `syscall.Errno(33)` as a transient Windows error alongside `syscall.Errno(5)` and `syscall.Errno(32)` (in addition to `errors.Is(err, os.ErrPermission)`).
- In `go/internal/ledger/ledger.go`, perform the atomic file replacement in `Save` using `fsretry.ReplaceWith(replaceFile, tempPath, target)` so transient replacement locks are retried while permanent errors still fail after a single attempt.
- When `Save` fails on a directory, temporary file, write/sync/close, or replacement error `err`, return `Result{Status: "UNKNOWN", Code: "LEDGER_WRITE_FAILED", Record: map[string]any{"error_class": fmt.Sprintf("%T", cause), "error": err.Error()}}`, where `cause` is obtained by repeatedly unwrapping `err` via `errors.Unwrap` until `errors.Unwrap(cause) == nil`.
