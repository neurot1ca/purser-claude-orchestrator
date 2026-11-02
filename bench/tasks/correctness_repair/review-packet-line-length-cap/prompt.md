# `WritePacket` produces lines over 2000 bytes when writing `review-packet.json`

## Problem

In `go/internal/review/packet.go`, `WritePacket(directory, name string, encoded []byte)` writes the encoded review packet directly to disk. When `name` is `"review-packet.json"`, long top-level string values (such as multi-line diffs, authority contracts, or long source lines containing multi-byte UTF-8 characters and JSON escapes) can exceed what downstream file readers handle reliably.

## Expected Behavior

When `name == "review-packet.json"`:
- For each top-level string value longer than 240 bytes, represent it as `{"concatenate_without_separator": [...]}`. Split the string into chunks of at most 240 runes, preserving all content and line endings so joining the chunks with `""` reconstructs the exact original string.
- Leave shorter top-level string values and non-string values unchanged.
- Return an error when `encoded` is not valid JSON.
