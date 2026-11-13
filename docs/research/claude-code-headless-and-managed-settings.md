# Claude Code headless / Agent SDK under managed settings

Ticket: AW-2. Researched 2026-11-12 from official docs only (code.claude.com). Local managed-settings files and ~/.claude were NOT read. Facts only, no recommendations.

Base URL for all cites: `https://code.claude.com/docs/en/` (abbreviated `D/`). Docs reflect Claude Code ~v2.1.3xx; many behaviors are version-gated (noted inline).

## 1. Driving Claude Code non-interactively

Three surfaces, same agent loop: CLI `claude -p`, Python SDK, TypeScript SDK. Agent SDK "runs the Claude Code binary" as a library (D/agent-sdk/overview.md). To drive from another language, run CLI as subprocess with `-p --output-format json` (same page).

### CLI print mode (D/headless.md, D/cli-reference.md)
- `claude -p "prompt"`; stdin piped (cap 10MB). Exit 0 success, non-zero on failure; SIGTERM -> exit 143, turn left unfinished, SessionEnd hooks still run.
- `--output-format text|json|stream-json`. json carries `result`, `session_id`, `total_cost_usd`, per-model cost. stream-json is NDJSON; needs `--verbose`; `--include-partial-messages` for token deltas; last line is a `result` message.
- `--input-format text|stream-json` (cli-reference). stream-json input = streaming multi-message input (detail of message shape not verified in this pass; see SDK streaming-vs-single-mode page, not fetched).
- `--json-schema` + json output -> `structured_output` field (absent if run ends without valid output).
- `system/init` event: model, tools, mcp_servers, plugins, `plugin_errors`, `mcp_server_errors`, `capabilities` array (feature-detect). Subagent msgs carry `parent_tool_use_id`; `--forward-subagent-text` adds text/thinking blocks.
- `system/api_retry` events; `permission_denied` system messages and `permission_denials` in final result (stream-json).
- Limits: `--max-turns`, `--max-budget-usd` (print only; client-side estimate, can overshoot), `--no-session-persistence`.
- `--bare`: skips hooks, skills, commands, subagents, plugins, MCP, auto memory, CLAUDE.md, OAuth/keychain (needs ANTHROPIC_API_KEY or apiKeyHelper). Docs say `--bare` is recommended for scripted calls and "will become the default for -p in a future release". Hooks passed via `--settings` still do not run in bare mode.
- Without `--bare`, `-p` loads project `.claude/settings.json` hooks and `.mcp.json` servers even in an untrusted folder; no trust dialog or per-server approval.
- Slash: user-invocable skills/commands work in `-p` via `/skill-name` in prompt; terminal-only built-ins do not.
- `--agents <json|file>` defines subagents; `--settings`, `--mcp-config`, `--append-system-prompt[-file]`, `--system-prompt`, `--plugin-dir` for config injection.
- Background work: `-p` stays open for background cmds/subagents/Monitor (10-min idle cap default, `CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`). Not for MCP background tasks.

### Sessions (D/agent-sdk/sessions.md, headless.md)
- CLI: `--continue` (most recent in cwd), `--resume <id|name|.jsonl path>`, `--fork-session`, `--session-id <uuid>`. Session id from json `session_id`.
- SDK options: `continue`, `resume`, `forkSession`, `persistSession:false` (TS only; Python uses `CLAUDE_CODE_SKIP_PROMPT_HISTORY`). Python `ClaudeSDKClient` auto-tracks; TS uses `continue: true`.
- Stored at `~/.claude/projects/<encoded-cwd>/*.jsonl` (or `$CLAUDE_CONFIG_DIR/projects/`). Same machine only unless a `SessionStore` adapter mirrors transcripts (D/agent-sdk/session-storage, not fetched). Sessions persist conversation, not filesystem (file checkpointing separate).
- Resume does not require same cwd from v2.1.223 (lookup across projects); older bundled CLIs scoped to project dir + worktrees.
- Subagents resumable: parse `agentId` from Agent tool result, resume parent session, pass same `agents` defs. Built-in Explore/Plan are one-shot (no agentId).

### Agent SDK (D/agent-sdk/overview.md, claude-code-features.md)
- Python `claude_agent_sdk.query()` / `ClaudeSDKClient`; TS `@anthropic-ai/claude-agent-sdk` `query()`. Capabilities listed: built-in tools, hooks, subagents, MCP, permissions, sessions, skills/commands/memory, plugins.
- `settingSources` / `setting_sources`: omitted == `["user","project","local"]` (loads same filesystem config as CLI). `[]` disables user/project/local only.
- Skills: discovered via settingSources; `skills` option `"all" | [names] | []`; setting it auto-adds Skill tool to allowedTools; with explicit `tools` list include `"Skill"`. No programmatic skill registration; must be `.claude/skills/<name>/SKILL.md`.
- Hooks: filesystem hooks (command/http/mcp_tool/prompt/agent types) fire in main agent and subagents; programmatic callback hooks via `hooks` option, also fire in subagents, input has `agent_id`/`agent_type`. TS supports more events (SessionStart, SessionEnd, TeammateIdle, TaskCompleted) than Python.
- Subagents (D/agent-sdk/subagents.md): `agents` option -> `AgentDefinition{description,prompt,tools,disallowedTools,model,skills,memory,mcpServers,maxTurns,background,omitClaudeMd,effort,permissionMode}`; or `.claude/agents/*.md`. Programmatic overrides same-name filesystem. Context is fresh: only the Agent-tool prompt string passes parent->child (plus CLAUDE.md unless omitClaudeMd). Subagents may spawn subagents; defaults: depth 3 (`CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH`), concurrency 20 (`CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS`); `maxBudgetUsd` covers subagent spend. Subagents run in background by default (`status: async_launched`). `AskUserQuestion` unavailable in subagents. Tool is named `Agent` in tool_use, `Task` in init tools list.
- Multi-tenant warning: default `query()` options read host-level config; for isolation use own filesystem + `settingSources: []` + `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` (claude-code-features.md).
- Auth note (overview.md): third-party developers may not offer claude.ai login/rate limits in products built on the SDK unless pre-approved; use API key auth.

## 2. Permission prompts when headless

Evaluation order (D/agent-sdk/permissions.md): 1 hooks -> 2 deny rules -> 3 ask rules -> 4 permission mode -> 5 allow rules -> 6 `canUseTool` callback.
- Deny rules (incl. from loaded settings files) win even in `bypassPermissions` and over hook `allow`.
- Hook `allow` does not skip deny/ask rules.
- Auto-approved calls (allow rule, acceptEdits, bypass) never reach `canUseTool`; use `PreToolUse` hook to see them. A hook deny applies even in bypass.
- Since v2.1.302, `-p` and SDK start in `default` (Manual) mode when nothing sets a mode. In Manual mode a call needing approval is DENIED unless something approves it: allow rule, `PermissionRequest` hook, `--permission-prompt-tool`, or SDK `canUseTool` (headless.md, permissions.md).
- Modes: `default`, `dontAsk` (every would-be prompt becomes deny, canUseTool not called), `acceptEdits`, `bypassPermissions`, `plan`, `auto` (classifier). Bypass refuses to start as root/sudo outside recognized sandbox; `allowed_tools` does not constrain bypass; `disallowed_tools` does.
- `canUseTool` (D/agent-sdk/user-input.md): returns allow (+`updatedInput`, optional `updatedPermissions`) or deny (+`message`, which Claude sees). Callback "can stay pending indefinitely". Handles `AskUserQuestion` too (answers returned via `updatedInput.answers`). Python needs streaming-input prompt plus a dummy PreToolUse hook to keep stream open (documented workaround).
- Long waits: a `PreToolUse` hook may return `defer` so process exits and resumes later from persisted session (user-input.md, hooks.md). Resume behavior of defer on `--resume` not explicit in fetched text.
- CLI equivalent: `--permission-prompt-tool mcp__<server>__<tool>` (MCP tool answers prompts; cannot approve MCP tools marked `requiresUserInteraction`). `--permission-prompts none` (v2.1.259+; TS option `permissionPrompts:'none'`): host/prompt-tool not consulted, anything that would prompt is denied unless a `PermissionRequest` hook allows; Claude told nobody can approve and not to retry; AskUserQuestion tool removed; MCP elicitation cancelled unless an `Elicitation` hook answers.
- `PermissionRequest` hook: output `hookSpecificOutput.decision.behavior = allow|deny`; exit 2 NOT honored for this event (hooks.md). Whether it fires in `-p`: headless.md explicitly names it as an approver for `-p`; hooks.md page did not state separately.
- Hook exit code 2 = block on blocking events; stderr becomes the message.
- Critical-path `rm`/`rmdir` removals and some actions are never auto-approved by any mode ("actions no mode auto-approves"); in `dontAsk` they are denied.
- Subagent inheritance: subagent inherits plan/acceptEdits/auto/bypass from parent; under default/dontAsk parent uses its own `permissionMode`; a `"bypassPermissions"` value is never applied to subagent (v2.1.267+). Plan mode in parent holds for subagents.
- SIGTERM while awaiting prompt: left unanswered; SDK closing session cancels prompt (headless.md).

## 3. Managed settings applied to spawned sessions

Source: D/managed-settings.md, D/settings.md, D/agent-sdk/claude-code-features.md.

- Managed tier is highest precedence: managed > `--settings` (CLI) > project local > project shared > user. Nothing in user/project/local/`--settings` overrides a managed key (settings.md). A few restrictive keys are honored from lower scopes even over managed (e.g. `permissions.blockReadsOutsideWorkingDirectories`, `permissions.blockWritesOutsideWorkingDirectories`, `permissions.denyInsteadOfPrompting` from user/--settings; full table in settings.md "Exceptions").
- Applies to SDK sessions: "Agent SDK sessions load managed settings even when `settingSources` excludes the user, project, and local files" (managed-settings.md; claude-code-features.md "What settingSources does not control"). Cannot be disabled from SDK; endpoint policy removed only by removing file/plist/registry. Server-managed settings (claude.ai console) fetched when session authenticates with qualifying org credential (OAuth login, configured API key, etc.); filesystem isolation does not remove them. `CLAUDE_CODE_OAUTH_TOKEN` from `setup-token` can only make model requests (connectors not loaded; server-managed fetch with it: not stated, uncertain).
- Delivery: managed-settings.json + optional `managed-settings.d/*.json` drop-ins + `managed-mcp.json` in system dir (macOS `/Library/Application Support/ClaudeCode/`, Linux/WSL `/etc/claude-code/`, Windows `C:\Program Files\ClaudeCode\`); MDM plist/registry; server-managed; `policyHelper` executable. Default across multiple admin sources: first source with a policy key wins; `managedSourcesBehavior: "merge"` merges. Within files: lists combine, nested blocks merge key-by-key (managed-settings.md).
- Embedding host (SDK `managedSettings` option -> "parent settings"): only host values that restrict are kept; host allow rules and sandbox allowlists still apply unless `allowManaged*Only` locks are set. `allowManagedPermissionRulesOnly` drops parent-supplied allow rules, `--allowedTools`, and settings-file allow rules (Cowork example) (managed-settings.md).
- Invalid/unparseable managed document: Claude Code refuses to start (also in `-p`) (managed-settings.md, settings.md). Single invalid restrictive keys read as their strictest value.
- Cloud sessions: device file/MDM not read; only server-managed. Self-hosted: reads file in runner image by default only when server-managed delivers no policy key.
- Locks (managed-only): `allowManagedPermissionRulesOnly`, `allowManagedHooksOnly` (blocks user/project/local/plugin hooks; managed hooks and force-enabled plugin hooks still run, including inside subagents), `allowManagedMcpServersOnly`, `sandbox.network.allowManagedDomainsOnly`, `sandbox.filesystem.allowManagedReadPathsOnly`, `permissions.disableBypassPermissionsMode`, `availableModels`/`deniedModels`, `strictKnownMarketplaces`, `strictPluginOnlyCustomization`.
- `permissions.defaultMode` `auto`/`bypassPermissions` are ignored from project/local settings (user, managed, or `--permission-mode` needed) (settings.md). Interaction of CLI `--permission-mode`/SDK `permissionMode` with `permissions.disableBypassPermissionsMode`: lock named, exact startup behavior not verified in this pass.

### Sandbox (D/sandboxing.md)
- Keys: `sandbox.enabled`, `failIfUnavailable` (default false; true = exit at startup if unsupported), `allowUnsandboxedCommands` (default true; false disables the `dangerouslyDisableSandbox` retry), `filesystem.{allowWrite,denyWrite,allowRead,denyRead,disabled}`, `network.{allowedDomains,deniedDomains,strictAllowlist,allowLocalBinding,...}`, `excludedCommands`, `credentials`.
- Sandbox wraps shell commands (Bash/PowerShell/Monitor) only. Not applied to Read/Edit/Write, WebFetch/WebSearch, MCP servers, hooks, LSP.
- "Admin-required" when managed sets `allowUnsandboxedCommands:false` or `network.allowManagedDomainsOnly:true`; then repo settings cannot override listed keys (excludedCommands, allowWrite, allowedDomains, Edit/WebFetch allow rules, etc.). Developer user settings still apply unless locked.
- `dangerouslyDisableSandbox` retry approval by mode: bypass runs without prompt; manual/acceptEdits prompts; auto -> classifier; dontAsk refused; a matching allow rule (e.g. `Bash(curl *)`) approves the retry regardless. Ask rule `Bash(dangerouslyDisableSandbox:true)` forces prompt.
- Headless specifics: sandbox page does not explicitly state whether `-p`/SDK enable sandbox by default (the fetch summary said so; not confirmed from primary text). SDK can pass `settings: {sandbox:{...}}`. Subagent sandboxing not explicitly documented. TREAT AS UNCERTAIN.
- Sandbox state in this environment's own system prompt is not a doc source and is not cited.

### MCP (D/managed-mcp.md)
- `managed-mcp.json` = exclusive control: only its servers + `managedMcpServers` + in-process SDK servers + (optionally Chrome). Cannot be delivered via server-managed settings. `--mcp-config` with it present: workstation exits with "You cannot dynamically configure MCP servers when an enterprise MCP config is present"; `--strict-mcp-config` also exits. Suppresses claude.ai connectors unless `allowAllClaudeAiMcps:true` (managed-only).
- `managedMcpServers` (v2.1.259+): remote http/sse servers provided alongside user servers; https only, no stdio, no `${VAR}`.
- `allowedMcpServers`/`deniedMcpServers`: entries `serverUrl` (wildcards), `serverCommand` (exact argv), `serverName` (exact; "not a security control"). Deny always wins and merges from all scopes. Allow unset = all allowed; `[]` = none (except those skipping the check). Without `allowManagedMcpServersOnly` allowlists merge across scopes (user can broaden). Both lists also filter `--mcp-config` servers; in-process `type:"sdk"` servers skip all checks.
- Non-managed MCP in headless: `-p` without `--bare` auto-connects `.mcp.json`; with `--mcp-config`, `-p` waits for pending servers up to `MCP_TIMEOUT` (30s default). Remote `managedMcpServers` arriving via server-managed settings on a machine's first launch may be missed by an already-running `-p` run; a `-p` run keeps a removed server until it ends.

## 4. Answers to the ticket's question, compressed
- Drive: `claude -p ... --output-format stream-json [--input-format stream-json]` or SDK `query()`. Session continuity via `session_id` + `--resume`/`resume`; fork supported.
- Managed settings: apply to every spawned session on the host, including SDK and `-p`, regardless of `settingSources`/`--bare`/`--settings`; they cannot be overridden by flags except where a restrictive exception exists. Not read by cloud sessions (device files) -- server-managed only.
- Headless permission prompt outcomes: deny by default; or resolved by allow rule, `PermissionRequest` hook, `--permission-prompt-tool`, SDK `canUseTool` (can wait indefinitely), or `PreToolUse` `defer`. `dontAsk` / `--permission-prompts none` force deny.

## 5. Open items / not verified
- `--input-format stream-json` message schema and control protocol (interrupts, `setPermissionMode`) not read from the streaming pages.
- Whether `-p`/SDK sessions get sandboxing by default; subagent sandbox behavior.
- `PreToolUse` `defer` exact resume semantics.
- Whether `claude -p` honors every managed lock identically to interactive (docs say managed applies to "all surfaces", no per-flag exceptions listed beyond table above).
- Server-managed settings fetch when authenticated only by `CLAUDE_CODE_OAUTH_TOKEN` or API key (docs mention "directly configured API key" qualifies; eligibility conditions at D/server-managed-settings.md#platform-availability not read).
- Primary-source repos (anthropics/claude-agent-sdk-typescript, -python, claude-code CHANGELOG) not read; only docs pages. Doc content is a live site and version-gated; features cited with minimum versions where docs gave them.

## Sources
- D/headless.md, D/cli-reference.md, D/settings.md, D/managed-settings.md, D/managed-mcp.md, D/sandboxing.md, D/hooks.md
- D/agent-sdk/overview.md, D/agent-sdk/claude-code-features.md, D/agent-sdk/permissions.md, D/agent-sdk/user-input.md, D/agent-sdk/sessions.md, D/agent-sdk/subagents.md
