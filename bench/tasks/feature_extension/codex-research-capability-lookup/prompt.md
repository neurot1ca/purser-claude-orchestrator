# Add structured Codex capability lookup for multiple model and effort candidates

## Problem

`go/internal/models/models.go` currently provides `CodexSupportsEffort(model, effort string) bool`, which checks only a single `(model, effort)` pair and collapses missing binaries, command failures, malformed output, absent models, and unsupported reasoning efforts into a single `false` boolean. Callers that need to evaluate a list of candidate `(model, effort)` pairs must distinguish between an untrusted/unavailable listing and specific per-candidate statuses using a single bounded listing call.

## Required Behavior

Add a structured batch capability lookup API in `go/internal/models/models.go`:

1. **Types and status constants:**
   - `CodexCapabilityCandidate`:
     ```go
     type CodexCapabilityCandidate struct {
         Model  string
         Effort string
     }
     ```
   - `CodexCapabilityStatus` (`string`) with constants:
     - `CodexCapabilityAvailable CodexCapabilityStatus = "available"`
     - `CodexCapabilityAbsent CodexCapabilityStatus = "absent"`
     - `CodexCapabilityUnsupportedEffort CodexCapabilityStatus = "unsupported_effort"`
     - `CodexCapabilityUnknown CodexCapabilityStatus = "unknown"`
   - `CodexCapability`:
     ```go
     type CodexCapability struct {
         Candidate CodexCapabilityCandidate
         Status    CodexCapabilityStatus
     }
     ```
   - `CodexCapabilityResult`:
     ```go
     type CodexCapabilityResult struct {
         Unknown    bool
         Candidates []CodexCapability
     }
     ```

2. **`CodexResearchCapabilities(candidates []CodexCapabilityCandidate) CodexCapabilityResult`:**
   - Resolve the `codex` listing command via `listingCommand("codex")` and run it once with a 2-second timeout context via `proc.Hide(exec.CommandContext(ctx, binary, args...))` and `runListingCommand(ctx, command)`.
   - If `binary == ""` (do not invoke `runListingCommand`), or `runListingCommand` returns an error, or `parseListing("codex", string(output))` returns an error, or JSON unmarshaling into the `models` listing fails or yields `listing.Models == nil`, return `CodexCapabilityResult{Unknown: true, Candidates: ...}` where every candidate has `Status: CodexCapabilityUnknown`.
   - When the listing is valid, return `Unknown: false` and evaluate each candidate in order against `listing.Models` entries that have `Visibility == "list"`:
     - `CodexCapabilityAbsent` if no visible model has `Slug == candidate.Model`.
     - `CodexCapabilityUnsupportedEffort` if a visible model matches `Slug == candidate.Model` but none of its `supported_reasoning_levels` has `Effort == candidate.Effort`.
     - `CodexCapabilityAvailable` if a visible model matches `Slug == candidate.Model` and has a reasoning level with `Effort == candidate.Effort`.

3. **`CodexSupportsEffort(model, effort string) bool`:**
   - Update `CodexSupportsEffort` to delegate to `CodexResearchCapabilities([]CodexCapabilityCandidate{{Model: model, Effort: effort}})` and return `!result.Unknown && result.Candidates[0].Status == CodexCapabilityAvailable`.
