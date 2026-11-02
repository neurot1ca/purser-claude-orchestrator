# Apply repository role overrides consistently when loading coder configuration

## Problem

Several parts of the application load coder configuration without the repository context, even though project role overrides are supported. As a result, commands and background checks can select a coder or reviewer from global configuration while operating on a repository with its own role settings.

## Requirements

Use repository-aware configuration loading in the existing paths that produce gear output, determine coder effort, select the host job's configured coder, perform readiness review, validate a start request's configured agent, and discover model information for a repository.

Resolve and pass the repository associated with each operation, including the repository resolved from a start request before choosing its configured agent. Preserve explicit agent choices, environment overrides, fixed-agent behavior, and the existing global configuration defaults and error handling. Model discovery that has no repository context must continue to load the non-project configuration.

## Interface

Call `policy.LoadCoderConfigForRepository(` in each of these files:

- `go/cmd/purser-hook/gear.go`
- `go/cmd/purser-hook/private.go`
- `go/internal/jobs/readiness_host.go`
- `go/internal/models/models.go`
