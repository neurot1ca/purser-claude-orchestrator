# Keep detached Purser children runnable from a stable executable copy

## Problem

Background child launchers and waiter commands resolve the running hook executable directly from its installed location. On Windows, a long-lived child can keep that installed binary locked and prevent the plugin directory from being updated.

## Requirements

Centralize executable resolution for the affected detached launch and waiter paths. Use a content-addressed copy beneath the Purser state root so those processes can run independently of the replaceable plugin directory. Reuse a valid existing copy, create copies safely for concurrent launchers, and fall back to the original executable if copying is unavailable so launch behavior remains resilient.

Prune only old sibling runtime copies during the existing cleanup path: retain the current copy and copies younger than the retention period, and tolerate copies that cannot be removed while in use.

## Interface

- In `go/internal/pluginroot/runtime.go`, define `func RuntimeExecutable(stateRoot string) (string, error)`, `func RuntimeCopy(stateRoot, executable string) string`, and a function with declaration prefix `func PruneRuntime(`.
- Call `pluginroot.RuntimeExecutable(` in `go/internal/waiter/command.go`, `go/internal/jobs/supervision.go`, and `go/internal/review/supervised.go`.
