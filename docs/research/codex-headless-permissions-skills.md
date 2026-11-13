# Codex CLI: headless use, permissions, skills (AW-3)

Researched 2026-11-12. Facts only. Sources fetched via WebFetch, which summarizes pages with a small model, so exact wording/paths should be re-verified against the page before being depended on.

Note: `developers.openai.com/codex/*` now 308-redirects to `learn.chatgpt.com/docs/*`. Treated as OpenAI's current docs host (redirect is server-supplied, host not independently verified).

## Sources
- [NI] https://learn.chatgpt.com/docs/non-interactive-mode (was developers.openai.com/codex/noninteractive)
- [SDK] https://learn.chatgpt.com/docs/codex-sdk
- [SEC] https://learn.chatgpt.com/docs/agent-approvals-security
- [SK] https://learn.chatgpt.com/docs/build-skills
- [AG] https://learn.chatgpt.com/docs/agent-configuration/agents-md
- [MC] https://learn.chatgpt.com/docs/enterprise/managed-configuration
- [EV] https://raw.githubusercontent.com/openai/codex/main/codex-rs/exec/src/exec_events.rs

## 1. Driving Codex non-interactively

`codex exec "<prompt>"` runs without the TUI [NI].
- Prompt input: argument; stdin piped plus argument (stdin becomes context); or `codex exec -` to read the whole prompt from stdin [NI].
- `--json`: JSON Lines to stdout. Event types documented: `thread.started`, `turn.started`, `item.completed`, `turn.failed` [NI]. Source enum `ThreadEvent` has: `thread.started`, `turn.started`, `turn.completed`, `turn.failed`, `item.started`, `item.updated`, `item.completed`, `error` [EV]. Item types: `agent_message`, `reasoning`, `command_execution`, `file_change`, `mcp_tool_call`, `collab_tool_call`, `web_search`, `todo_list`, `error` [EV].
- `--output-schema <path>`: final response must match a JSON Schema. `-o, --output-last-message <path>`: write final message to file [NI].
- `--ephemeral`: do not persist session to disk. `--skip-git-repo-check`: allow non-git dirs. `--ignore-user-config`: skip `$CODEX_HOME/config.toml`. `--ignore-rules`: skip user/project rule files. `--approve-for-me`: route approvals through automatic review [NI].
- Resume: `codex exec resume --last "next task"` or `codex exec resume <SESSION_ID>` [NI].
- Auth in automation: `CODEX_API_KEY=<key> codex exec --json ...` (inline per invocation recommended). Official GitHub Action: github.com/openai/codex-action [NI].
- `--full-auto` is deprecated; use `--sandbox workspace-write` or `--approve-for-me` [NI].

### SDKs and app server [SDK]
- TypeScript: `npm install @openai/codex-sdk`, Node 18+. `new Codex()`, `startThread()`, `thread.run(prompt)`, `resumeThread(threadId, options)`. Settings via `config` (structured) or `configOverrides` (raw TOML strings).
- Python: `pip install openai-codex`, Python 3.10+. `Codex()` context manager, `thread_start(model=...)`, `thread.run(prompt)`, `thread_resume(thread_id)`, `AsyncCodex`. Sandbox presets `Sandbox.read_only`, `Sandbox.workspace_write`, `Sandbox.full_access`.
- App server: for custom clients that handle auth, history, approvals, event streaming. Legacy `codex mcp-server` is deprecated in favor of it.
- Not verified: whether `run` streams events (the TS `runStreamed` name was not confirmed in fetched text); the SDKs' relation to the `codex exec` process (not stated in fetched text).

## 2. Sandbox / approval model [SEC][NI]
- Sandbox modes: `read-only`, `workspace-write`, `danger-full-access` (no sandbox, no approvals).
- Approval policies: `on-request`, `never`, `{ granular = {...} }` (selective prompts, others auto-rejected).
- Defaults: SEC summary says interactive default is `workspace-write`; NI says `codex exec` default sandbox is read-only. Treated as consistent (exec differs from interactive) but not confirmed explicitly.
- Protected paths stay read-only inside writable roots: `.git` (and gitdir pointers), `.agents`, `.codex`, `.aws` [SEC].
- Network: off in workspace-write unless `[sandbox_workspace_write] network_access = true`. Domain policy: exact hosts, `*.example.com`, `**.example.com`, `*`; `deny` always wins [SEC].
- Presets: `--sandbox workspace-write --ask-for-approval on-request`; `--dangerously-bypass-approvals-and-sandbox` (alias `--yolo`) [SEC].
- Auto review: `approval_policy = "on-request"` + `approvals_reviewer = "auto_review"`; reviewer checks exfiltration, credential probing, destructive actions; fails closed on error/timeout [SEC].
- Not found: how a headless `exec` run surfaces an approval request that would normally prompt (with `on-request` and no TTY). Not stated in fetched text; needs testing or source reading.

## 3. Admin-managed config equivalent [MC]
- `requirements.toml`: enforced constraints users cannot override. Keys listed: `allowed_approval_policies`, `allowed_sandbox_modes`, `allowed_web_search_modes`, `allowed_permission_profiles`, `allowed_approvals_reviewers`, `mcp_servers` (allowlist), `[rules]` (merge with regular `.rules` files), `[permissions.filesystem]`, `deny_read`, `[browser_use]`, `[computer_use]`, `features` table. Top-level `allow_managed_hooks_only = true` ignores user/project/session hooks (works only in `requirements.toml`).
- `managed_config.toml`: starting defaults applied at client launch; users can change them during a run. Legacy managed defaults take precedence above local config at startup.
- Precedence: cloud requirements higher than system requirements; then OS-specific env override, all-OS env override, global.
- macOS MDM: `requirements_toml_base64` key.
- UNCERTAIN: fetched summary gave file locations as `~/.codex/requirements.toml` and `~/.codex/managed_config.toml` (Windows `%USERPROFILE%\.codex\`). That reads like user-level paths; a system path such as `/etc/codex/` was expected from prior knowledge but is not confirmed in the fetched text. Re-check the page. Skills doc does list `/etc/codex/skills` as the admin skills scope [SK].

## 4. Skills [SK]
- Codex supports Agent Skills: a directory with required `SKILL.md` (fields `name`, `description`) plus optional scripts, references, assets.
- Scan locations: REPO `.agents/skills` (cwd up to repo root); USER `$HOME/.agents/skills`; ADMIN `/etc/codex/skills`; SYSTEM built-in (e.g. `skill-creator`). Name conflicts are not merged; both can appear.
- Invocation: explicit (`$skill-name` or `@` mention) or implicit (model chooses by description).
- Progressive disclosure: initial skill list limited to 2% of context window (8,000 chars if window unknown); full body loads on selection.
- Optional `agents/openai.yaml`: UI metadata, `allow_implicit_invocation` policy, tool dependencies.
- Not verified: behavior of skills under `codex exec` specifically (assumed same loader; not stated).

## 5. AGENTS.md [AG]
- Global: `~/.codex/AGENTS.override.md` then `~/.codex/AGENTS.md` (`CODEX_HOME` env selects alternate dir).
- Project: from Git root down to cwd, at each level `AGENTS.override.md`, then `AGENTS.md`, then `project_doc_fallback_filenames` (config.toml).
- Merge: concatenated root-down, joined by blank lines; closer files appear later (override earlier). Stops at `project_doc_max_bytes` (default 32 KiB). Empty files skipped.
- Rebuilt every run. Check: `codex --ask-for-approval never "Summarize the current instructions."`
- Note: `.claude`-style `CLAUDE.md` is not read unless added to `project_doc_fallback_filenames` (inference from the fallback mechanism, not stated).

## Open items
- Exact admin file paths (section 3).
- Headless approval behavior (section 2).
- Primary-source cross-check on github.com/openai/codex (`docs/` there only links to the hosted docs; only `exec_events.rs` was read from source).
