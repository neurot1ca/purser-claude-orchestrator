# Keep detached Purser children runnable from a stable executable copy

## Problem

Background child launchers and waiter commands resolve the running hook executable directly from its installed location. On Windows, a long-lived child can keep that installed binary locked and prevent the plugin directory from being updated.

## Requirements

Centralize executable resolution for the affected detached launch and waiter paths. Use a content-addressed copy beneath the Purser state root so those processes can run independently of the replaceable plugin directory. Reuse a valid existing copy, create copies safely for concurrent launchers, and fall back to the original executable if copying is unavailable so launch behavior remains resilient.

Store copies as `<stateRoot>/runtime/<content-address>/<original-basename>`, with executable permissions `0755` on non-Windows platforms. Reusing a valid copy must leave its file identity and modification time unchanged. Apply this resolution to the mechanical-shadow launcher in `go/internal/enforce/post_mechanical.go` as well as the waiter and detached launch paths.

During the existing cleanup path, prune only sibling runtime directories whose modification time is older than the current copy's directory and whose age is at least 72 hours. Retain the current copy, all newer siblings regardless of age, and copies younger than 72 hours; tolerate copies that cannot be removed while in use.

## Interface

- In `go/internal/pluginroot/runtime.go`, define `func RuntimeExecutable(stateRoot string) (string, error)`, `func RuntimeCopy(stateRoot, executable string) string`, and `func PruneRuntime(stateRoot, current string, now time.Time)`.
- Call `pluginroot.RuntimeExecutable(` in `go/internal/waiter/command.go`, `go/internal/jobs/supervision.go`, and `go/internal/review/supervised.go`.
