# Reject non-gitlink tracked files replaced by directories during validation checkout snapshot

## Problem

In `go/internal/jobs/apply_validation.go`, `checkoutRevisionOnce(repo string, backoff *[]time.Duration) (string, string, error)` snapshots the working tree into a temporary Git index (`GIT_INDEX_FILE`) by running `git add -A`, `git add --renormalize -- .`, and `git write-tree`.

When a regular tracked file in the repository is removed and replaced on disk by a directory at the same path (even if that directory contains untracked files), `git add -A` and `git add --renormalize -- .` do not report an error when staging into the temporary index, allowing `checkoutRevisionOnce` to succeed instead of failing validation. At the same time, gitlink entries in the index (mode `160000 `) legitimately correspond to directories on disk and must not be rejected.

## Expected Behavior

Before running `git add -A` on the temporary index in `checkoutRevisionOnce`:
- Inspect deleted and modified paths in the temporary index (`git ls-files --deleted --modified -z`).
- If any paths are reported, inspect the staged index entries (`git ls-files --stage -z`).
- For each staged entry whose path is in the deleted/modified set and whose stage header does **not** begin with `"160000 "` (i.e. not a gitlink), `os.Lstat` the path on disk inside `repo`.
- If `os.Lstat` returns an error other than `os.ErrNotExist`, return that error.
- If `os.Lstat` succeeds and `info.IsDir()` is `true`, return an error `fmt.Errorf("tracked file replaced by directory: %s", path)`.
- Allow deleted tracked files (where `os.Lstat` returns `os.ErrNotExist`) and gitlink (`160000 `) directories to proceed normally.
