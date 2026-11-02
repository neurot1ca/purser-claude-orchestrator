# Recursive directory `/**` scopes falsely flag nested files as out-of-scope and mandatory bundle fallback drops `changed_paths` preview

## Problem

Two bugs affect scope checking and compact bundle generation:

1. In `go/internal/ledger/out_of_scope.go`, `DetectOutOfScopeModifications` / `pathInScope` only matches `.`, exact path equality, or `<scope>/` prefix after trimming trailing `/`. It does not match recursive glob scopes (`<dir>/**` or `"**"`), does not normalize `\` path separators to `/`, and does not compare paths case-insensitively on Windows. Consequently, nested files under a `<dir>/**` scope (or scopes using backslashes) are falsely reported as out-of-scope modifications.
2. In `go/internal/enforce/bundle.go`, `compactBundle` builds a preview of up to 10 `changed_paths` items, but when the compacted bundle still exceeds `cap` and falls back to the `mandatory` map, it resets `mandatory["changed_paths"]` to an empty slice `[]any{}` instead of preserving the up-to-10-item `changed_paths` preview alongside `changed_path_count`.

## Expected Behavior

- In `go/internal/ledger/out_of_scope.go`, normalize `\` separators to `/` (and compare case-insensitively on Windows) in `pathInScope`, and treat `"**"` as matching any path and `<dir>/**` as matching `<dir>` or any nested path under `<dir>/`, while continuing to flag paths outside `<dir>`.
- In `go/internal/enforce/bundle.go`, retain the up-to-10-entry `changed_paths` preview slice in `mandatory["changed_paths"]` when `compactBundle` falls back to the `mandatory` representation.
