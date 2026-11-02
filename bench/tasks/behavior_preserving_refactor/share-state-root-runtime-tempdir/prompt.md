# Share runtime temporary directory handling for probes and reviews

## Problem

The API probe, Reviewer-1, and readiness check each create an isolated temporary working directory directly in the operating system's default temp location. Their setup is duplicated and does not first keep runtime scratch data with the configured Purser state.

## Requirements

Consolidate temporary-directory creation for these three paths. First create the directory under the state-root cache temp area, creating that parent when needed; if that location cannot be created or used, fall back to the operating system's default temporary directory. Preserve each caller's existing directory prefix, isolated-workdir behavior, and cleanup behavior. Return an error only when temporary-directory creation fails in both locations.

## Interface

- Define a function with declaration prefix `func runtimeTempDir(` in both `go/internal/enforce/runtime_temp.go` and `go/internal/review/runtime_temp.go`.
- In `go/internal/enforce/probe.go`, call `runtimeTempDir("purser-api-probe-")` and remove `os.MkdirTemp("", "purser-api-probe-")`.
- In `go/internal/review/native.go`, call `runtimeTempDir("reviewer1-")`.
- In `go/internal/review/readiness.go`, call `runtimeTempDir("readiness-check-")`.
