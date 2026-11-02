# `WritePacket` produces lines over 2000 bytes when writing `review-packet.json`

## Problem

In `go/internal/review/packet.go`, `WritePacket(directory, name string, encoded []byte)` writes the encoded review packet directly to disk. When `name` is `"review-packet.json"`, downstream file readers enforce a 2000-byte per-line limit, so single-line JSON output or long string values (such as multi-line diffs, authority contracts, or long source lines containing multi-byte UTF-8 characters and JSON escapes) exceed 2000 bytes on a single line.

## Expected Behavior

When `name == "review-packet.json"`:
- Ensure no line in the written `review-packet.json` file exceeds 2000 bytes.
- Preserve short string fields as plain JSON strings, and represent long string fields as `{"concatenate_without_separator": [...]}` where joining the string array elements with `""` losslessly reconstructs the exact original string (including all line endings, escapes, and multi-byte runes).
- Return an error if `encoded` is not valid JSON for a packet object.
