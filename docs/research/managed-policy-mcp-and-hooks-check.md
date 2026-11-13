# Does managed policy block injected MCP servers and hooks? (AW-21)

Run 2026-11-12 on this machine, Claude Code and `codex-cli 0.162.1`. Facts observed. Scope of what was read, per the developer's approval: the file-based Claude Code managed policy locations only. Nothing was read from `~/.claude` or any policy cache; the rest was done by running the CLIs with injected config and watching what they did. Probe files and directories were deleted afterwards.

## What exists on this machine

- `/Library/Application Support/ClaudeCode/` does not exist: no `managed-settings.json`, no `managed-settings.d/`, no `managed-mcp.json`.
- `defaults read com.anthropic.claudecode` reports the domain does not exist (no MDM plist for Claude Code).
- So any Claude Code managed policy here is not file-based. Behavior (below) shows enterprise policy is nonetheless in force, which matches server-managed settings; the delivered values were not read.

## Claude Code under `claude -p` (empirical)

1. **Injected MCP servers are blocked.** `claude -p --mcp-config '<json>'` with a stdio server, and again with stdio, `http` and `sse` servers together, printed `Warning: MCP server(s) blocked by enterprise policy: <names>`. None appeared in the init event's `mcp_servers`. The process exited 0 and the prompt still completed.
2. **Already-configured servers still connect.** The init event listed the developer's own user-level servers (`bitbucket-mcp-server`, `claude.ai Atlassian Rovo v2`, Google Drive, Calendar, Gmail) as `connected` and others as `needs-auth`. A warning also said `claude.ai MCP servers blocked by enterprise policy: claude.ai Meetings, claude.ai Jira`. So the policy behaves as an allowlist: some servers permitted, injected ones refused, whatever mechanism names the allowed ones.
3. **Hooks supplied by `--settings` run.** A `UserPromptSubmit` command hook injected through `--settings` created its marker file. Hooks are not restricted to managed-only here.
4. **Effective permission mode.** The init event for `-p` reported `permissionMode: auto`, not `default`. The runner's default on this machine is therefore whatever the developer's own settings produce, and "no more permissive than the runner's default" (ADR 0011) is measured against that.
5. Not tested: project `.mcp.json` servers, the in-process `type: "sdk"` servers of the Agent SDK (the documented exception to MCP checks; the factory does not embed the SDK, ADR 0003), and whether an allowed server *name* could be reused (deliberately not attempted).

## Codex under `codex exec` (empirical, inconclusive)

- `codex exec -c mcp_servers.<name>.command=...` with a dummy stdio command ran, exited 0 and produced no mention of the server in stdout or stderr, neither a policy notice nor a startup error. Whether the server was blocked, ignored or started and failed silently was not determined. A real MCP server would be needed to tell. The cloud requirements bundle was not read for `mcp_servers` or hook restrictions (outside the approval given).

## Consequence stated as fact

On this machine the Works cannot attach its own MCP server to a headless Claude Code session via `--mcp-config` (it is refused by enterprise policy), whereas command hooks supplied through `--settings` do execute. Delivery of tools to agents by Bash commands, hooks or any already-allowed server was not evaluated.
