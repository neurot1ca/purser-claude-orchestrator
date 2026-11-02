# Disable inherited MCP servers, connectors, and browser tools on read-only launches

## Problem

When launching read-only agent commands in `go/internal/jobs/agents.go` and `go/internal/jobs/launch_args.go`, inherited MCP servers, app connectors, plugins, and browser/computer-use integrations from the user's configuration can remain active and expose write-capable tools during a read-only run.

## Required Behavior

1. **Read-only Claude launches (`BuildAgentCommand` in `go/internal/jobs/agents.go`):**
   - When `readOnly` is true for agent `"claude"`, append the flags `"--safe-mode"`, `"--strict-mcp-config"`, `"--mcp-config"`, `{"mcpServers":{}}`, `"--no-chrome"` immediately after `"--setting-sources", "", "--restricted"`.
   - Non-read-only Claude launches must not include these flags and continue to pass `"--permission-mode", "bypassPermissions"`.

2. **Read-only Codex launches (`BuildCodexCommand` in `go/internal/jobs/launch_args.go`):**
   - When `args.Sandbox == "read-only"` (and not for other sandbox modes such as `"workspace-write"`), append `-c` configuration overrides to disable external features:
     - `features.apps=false`
     - `features.plugins=false`
     - `features.browser_use=false`
     - `features.browser_use_external=false`
     - `features.computer_use=false`
   - Also discover and disable any MCP servers defined in the active Codex TOML config files:
     - Determine the Codex config directory from `CODEX_HOME`: if `CODEX_HOME` is non-empty and relative, resolve it relative to `args.Cwd`; if `CODEX_HOME` is empty, use `<os.UserHomeDir()>/.codex`.
     - Read `config.toml` in that directory. If the resolved command `prefix` (after index 0) contains `--profile <name>`, `-p <name>`, or `--profile=<name>`, also read `<profile>.config.toml` in that directory.
     - Ignore missing, unreadable, or invalid TOML files without failing `BuildCodexCommand`.
     - Collect all keys under the `mcp_servers` table across the readable config files in sorted order.
     - Encode each server name as a TOML key (`(toml.Key{name}).String()`):
       - For bare keys where the encoded key equals `name`, emit `-c mcp_servers.<name>.enabled=false`.
       - For keys that require quoting (e.g. names containing dots or spaces), combine all quoted keys into a single override `-c mcp_servers={<quoted1>={enabled=false},<quoted2>={enabled=false}}` placed before the bare `mcp_servers.<name>.enabled=false` overrides.
