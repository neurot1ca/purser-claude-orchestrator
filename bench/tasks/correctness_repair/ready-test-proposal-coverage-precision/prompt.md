# Ready test-proposal notice suppresses or misformats sub-percent coverage increases

## Problem

When `formatMaintenanceNotice` in `go/internal/contextio/maintenance_notice.go` renders a `"test-gaps"` maintenance notice containing a ready `test_proposal`, it rounds the `before` and `after` coverage percentages to nearest whole integers before checking whether coverage increased and before formatting the `"Coverage would rise from ... to ..."` summary line.

Because of this premature rounding, a ready proposal that improves coverage by a fraction of a percent within the same rounded whole number (for example, from `801/1000` to `804/1000`, which both round to `80%`) is treated as having no improvement and suppressed (`""`).

## Expected Behavior

- Evaluate whether a ready test proposal improves coverage (`after > before`) using the unrounded coverage percentages rather than the rounded whole percentages.
- When the rounded whole percentages for `before` and `after` differ (for example, `80.2%` to `85.0%`), continue displaying rounded whole percentages (`80%` to `85%`).
- When coverage increases but the rounded whole percentages for `before` and `after` are equal (for example, `80.1%` to `80.4%`), display both percentages with one decimal place (`80.1%` to `80.4%`) in the `"Coverage would rise from ... to ..."` notice line.
