# Task labels over 80 runes bypass truncation when non-plan or already suffixed

## Problem

`planMilestoneLabel` in `go/internal/enforce/pre_decision.go` formats string task labels and enforces an 80-rune maximum length, appending ` [<prefix>-<m>/<total>]` for plan milestone task IDs (`<prefix>-m<m>of<total>`).

Currently, several cases bypass the 80-rune cap or leave surrounding whitespace untrimmed:
1. Leading and trailing whitespace on string labels is not trimmed before length checks, and non-plan task IDs return the input label without enforcing the 80-rune cap.
2. When `taskID` is a plan milestone ID and `label` already ends with the milestone suffix ` [<prefix>-<m>/<total>]`, `planMilestoneLabel` returns `label` immediately without checking whether the total length exceeds 80 runes.

## Expected Behavior

- Leave non-string labels unchanged. For string labels, trim leading and trailing whitespace first and return `""` if the result is empty.
- For non-plan task IDs, cap the label at 80 runes and trim any trailing whitespace left by truncation.
- For plan milestone task IDs (`<prefix>-m<m>of<total>`), always enforce the 80-rune total limit (including the single ` [<prefix>-<m>/<total>]` suffix), even when the input label already ends with that suffix, by truncating the prefix before the suffix and trimming trailing whitespace from the prefix before attaching the suffix.
