# Purser privacy policy

Last updated: 2026-10-18

Purser is a Claude Code plugin that runs on your computer. It has no server of its own and no accounts, and it collects no personal data. Nothing it records is sent to the Purser authors.

## What Purser reads

- The repository you work in.
- The Claude Code hook events of your session: tool calls, prompts, and session and tool-use IDs.
- Your session's usage and cost figures, which it shows in its status band.
- Its own state files.

## What Purser sends, and where

- **To the coder you chose.** Purser sends each worker's brief, and the repository content that worker reads, to that coder's provider: OpenAI for Codex, Google for agy, Cursor, the provider you pick in OpenCode, or Anthropic when Claude is the coder. The coder CLI sends it under that provider's terms, using your own sign-in.
- **models.dev.** Purser downloads the public model list to rank models. It sends no data of yours.
- **GitHub.** Once a day Purser checks for a newer release. It sends no data of yours. Set `PURSER_NO_UPDATE_CHECK=1` to turn the check off.

Purser makes no other network requests. It has no analytics and no telemetry upload.

## What Purser stores, and for how long

Purser keeps job records, plans, handoff notes, logs, measurements and job worktrees in `~/.claude/purser/`, or in `PURSER_STATE_DIR` when you set it. All of it stays on your machine. `/purser:init` saves the project's settings to `.orchestrator/profile.toml` in your repository. If you name an Obsidian vault, Purser also writes telemetry notes there.

Purser deletes its own data on a schedule. It removes raw tool outputs and review bundles after 7 days, and a finished job's worktree once that job is closed. Other records go when they pass an age or size limit. To remove everything at once, uninstall the plugin and delete the state folder.

## Credentials

Purser does not read, store or send your passwords, tokens or API keys. Each coder CLI uses its own sign-in.

## Code Purser runs

Besides its MCP server, Purser's Claude Code hooks start its bundled binary, `bin/purser-hook-<os>-<arch>`. That binary starts `git` and the coder CLIs you set up. The README section "What Purser does on your machine" describes each of them.

## Contact

Open an issue at https://github.com/neurot1ca/purser-claude-orchestrator/issues.
