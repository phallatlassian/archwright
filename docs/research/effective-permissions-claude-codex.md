# Effective headless permissions on this machine: Claude Code vs Codex

Researched 2026-11-17. Machine: `claude 2.1.285`, `codex-cli 0.162.1` (per docs/research/headless-managed-permissions.md). Read-only inspection; no secrets reproduced. WebFetch results are small-model summaries; re-verify wording before depending on it. Unconfirmed = UNVERIFIED.

Refs: `CC/` = https://code.claude.com/docs/en/ ; `CX/` = https://learn.chatgpt.com/docs/ ; `RS` = `~/.claude/remote-settings.json`; `P` = `docs/research/headless-managed-permissions.md`; `A` = `docs/research/codex-headless-approvals-test.md`; `C2` = `docs/research/codex-headless-permissions-skills.md`; `H` = `docs/research/claude-code-headless-and-managed-settings.md`.

Headless step runs use `dontAsk` (anything that would prompt is denied) (P §2; ADR 0011). Claude "no prompt" below means: allowed under dontAsk. The user's own default is `auto` (classifier decides), which is looser and not what Archwright uses.

## 1. Claude Code: managed policy found (RS)

- File policy absent: `/Library/Application Support/ClaudeCode/` and `managed-settings.d/` do not exist (checked). Policy is server-managed: RS (+ signatures).
- RS top-level keys: `allowManagedMcpServersOnly`, `allowManagedPermissionRulesOnly`, `allowedMcpServers`, `deniedMcpServers`, `autoMode`, `autoUpdatesChannel`, `companyAnnouncements`, `disableAllHooks`, `disableBrowserExternalNavigation`, `enableAllProjectMcpServers`, `env`, `permissions`, `sandbox`, `strictKnownMarketplaces`.
- `allowManagedPermissionRulesOnly: true`: user/project/local/`--settings` allow/ask/deny rules and `--allowedTools` are ignored (CC/settings-reference#allowmanagedpermissionrulesonly, quoted in P §1). So the user rule `Bash(archwright tool:*)` in `~/.claude/settings.json` does not count (P §0 tension).
- `permissions.allow` (~105, managed only). Non-MCP entries:
  - Unscoped: `Edit`, `Write`, `Agent`.
  - Bash wildcard: `twg *`, `ping *`, `nslookup *`, `host *`, `mkdir *`, `tmux *`, `ssh *`, `scp *`, `curl *`, `uv *`, `python *`, `python3 *`, `npm *`, `node *`, `pytest *`, `cp *`, `cat *`, `grep *`, `find *`, `tail *`, `head *`, `ls *`, `echo *`, `bb *`, `databricks *`.
  - Bash exact (no `*`): `git diff`, `gh issue view`, `gh issue list`, `gh issue status`, `gh pr view`, `gh pr list`, `gh pr status`, `gh project view|link|list`.
  - Read: a few `.git/*` files, `~/.cache/uv/**`, gradle lock, `~/.config/claude/jobs/**`. Rest are MCP read tools (bitbucket, atlassian, twg, leyline).
  - No `archwright`, `git status|log`, `pnpm`, `go`, `rg`, `WebFetch` entries.
  - Matching caveat: "Bash rules match the whole command text, with `*` standing in for any text" (CC/permissions#bash). So exact `gh issue view` likely does NOT match `gh issue view 75`; `git diff` likely does not match `git diff --stat`. UNVERIFIED (not tested); `git diff` is covered anyway by the built-in read-only set.
- `permissions.deny` (~31): `Read` of `.env*`, `secrets/**`, `*.pem`, `*.key`, `~/.ssh`, `~/.aws`, `~/.gnupg`, `/etc/passwd|group`; `Bash(sudo *)`; MCP writes (tableau, conveyor, ironclad). No `ask` list.
- `sandbox`: `enabled: true`; no `autoAllowBashIfSandboxed` key, no `excludedCommands`, no `allowUnsandboxedCommands` key. Default for the auto-allow key UNVERIFIED (docs describe it only as an opt-in "sandbox auto-allow mode", CC/sandboxing; user `~/.claude/settings.json` has no `sandbox` key).
  - `filesystem.allowWrite`: `~/Library/Application Support/rtk`, Secretive ssh socket, `~/.jira`. `allowRead`: SystemVersion.plist, ssl cert.pem, `~/.ssh/config`, `~/.ssh/known_hosts`. `denyRead`: `/System`, `/etc/passwd`, `/etc/group`.
  - `network`: `allowManagedDomainsOnly: false`; `allowedDomains` 175 entries incl. `github.com`, `*.github.com`, `*.githubusercontent.com`; no npm/PyPI/Go proxy hosts matched (grep for github|npm|pkg|registry|golang|proxy); `deniedDomains: atlassiansupport.atlassian.net`; `allowUnixSockets`: Secretive socket only.
- Hooks: `disableAllHooks: false` (hooks permitted). MCP: `allowManagedMcpServersOnly: true` (injected MCP refused; ~2000 `deniedMcpServers` entries), `enableAllProjectMcpServers: false` (P §0; docs/research/managed-policy-mcp-and-hooks-check.md).
- `env`: telemetry/updater toggles only. `autoMode.environment`: prose hints for classifier (trusted source control, domains). Only matters in `auto`.

## 2. Claude Code: built-in rules (CC/permissions, saved fetch `.../tool-results/toolu_011po6MEvJHLeyYkL2dENSFa.txt` L215-340)

- Read-only Bash set runs without prompt in every mode: `ls, cat, echo, pwd, head, tail, grep, find, wc, which, diff, stat, du, cd`, "and read-only forms of `git`". Not configurable. Full list not in the doc text fetched (UNVERIFIED beyond those names; `rg` UNVERIFIED).
- Unquoted globs OK for commands whose flags are all read-only; prompt for `find|sort|sed|git` with unquoted globs. `find -exec/-delete/-fprint/-files0-from` prompt. Unparseable or >10,000 char commands prompt. Writes to `PATH`/`IFS` prompt.
- Compound: separators `&&, ||, ;, |, |&, &`, newline. "Every subcommand must be allowed" (rule or read-only). Trailing `&&` with nothing after: unparseable, no rule matches. Deny/ask match any subcommand, incl. subshells/`$(...)`.
- Wrappers stripped before matching: `timeout, time, nice, nohup, stdbuf, command, builtin, noglob`; bare `xargs`; leading known-safe env vars (`NODE_ENV=test npm test` ok; other vars not). Not stripped: `npx`, `docker exec`, `direnv exec`, `bash -c`, `sh -c`.
- `cd` in working dir (or added dir) is read-only; `cd x && ls` ok. `cd` + `git` into a different dir prompts. `cd` + redirect prompts unless target `/dev/null`.
- Redirects: `> f`, `>> f`, `2> f` checked against Edit allow/deny, protected paths, working dirs; target starting `~` or containing glob prompts. `< f` outside working dirs prompts (v2.1.257+). `tee` targets checked (v2.1.269+). `/dev/null`, `2>&1`, heredocs not checked.
- Files: Read/Grep/Glob in working dir + added dirs: no prompt (CC/permissions table L17). Edit/Write: prompt by mode (`acceptEdits` auto-accepts in working dir); here managed unscoped `Edit` + `Write` allow rules cover all paths, subject to deny rules and protected paths (CC/permission-modes#protected-paths: not fetched; UNVERIFIED which). File tools are not sandboxed (P §3).
- WebFetch: prompts except domains with an allow rule and a built-in preapproved docs-domain set (list not published in fetched text, CC/tools-reference#webfetch). No managed `WebFetch` allow here, so WebFetch to github.com: not allowed under dontAsk (inference). Sandboxed commands do not inherit the preapproved set (CC/tools-reference, same section).
- Sandbox auto-allow (`autoAllowBashIfSandboxed`) if it were on (CC/sandboxing, summary): sandboxed Bash approved with no prompt unless a deny rule, content-scoped ask rule, critical-path `rm`/`rmdir`, or env-var-assignment/`eval` command; unsandboxed commands (excludedCommands, retry) go through normal flow. Not applicable here unless the user enables it; Archwright must not write it (ADR 0011).
- Sandbox defaults for Bash (CC/sandboxing): write = cwd + subdirs, `$TMPDIR`, `--add-dir` dirs, worktree `.git`, plus managed `allowWrite`; protected paths (`.claude` settings, shell rc, `.git/config`) deny writes. Read = most of machine, minus managed `denyRead`. Network = proxy allowlist (`allowedDomains`).
- Unsandboxed retry under `dontAsk`: denied (P §2; CC/sandboxing#the-unsandboxed-retry-escape-hatch).

## 3. Claude Code table (headless, `dontAsk`, managed rules only)

| Action | No prompt? | Why | Source |
|---|---|---|---|
| `git status`, `git log`, `git diff`, `git show` | Yes | built-in read-only git forms; `git diff` also managed exact allow | CC/permissions#read-only-commands; RS |
| `ls`, `cat`, `grep`, `find`, `head`, `tail`, `wc`, `pwd` | Yes | built-in read-only (+ managed `cat *` etc.) | same; RS |
| `rg` | UNVERIFIED | not named in doc list; not in managed allow | CC/permissions |
| `gh issue view 75` (needs bare form) | Probably no for arguments; yes for bare `gh issue view` | managed rules are exact strings, no `*` | RS; CC/permissions#bash |
| `gh issue view` network | Fails in sandbox even if allowed | Go TLS fail under Seatbelt (`x509 OSStatus -26276`), #73; github.com is in allowedDomains | CC/sandboxing; P §3 |
| `archwright tool <name>` | No (denied) | no managed allow; user rule ignored by `allowManagedPermissionRulesOnly`; not read-only. Passes only in `auto` via classifier (UNVERIFIED) | RS; P §0, Open Q1 |
| Piped read-only `git log \| head` | Yes | every subcommand read-only/allowed | CC/permissions#compound-commands |
| Compound `cd sub && ls` in repo | Yes | `cd` in working dir read-only | CC/permissions |
| Compound `git status && archwright tool x` | No | one subcommand unallowed | same |
| `cd other && git status` | No | cd+git into different dir prompts | same |
| Edit/Write tool in repo | Yes | managed unscoped `Edit`/`Write` allow | RS |
| Edit/Write tool to spool root | Yes (rule) | same, unscoped; protected paths UNVERIFIED | RS |
| Bash `echo x > file` in repo | Yes | managed `echo *`; redirect inside working dir; sandbox write cwd | RS; CC/permissions#redirections |
| Bash write to spool root `/tmp/claude-<uid>/archwright` | Yes | sandbox write incl. per-user temp; matches `launch.SpoolRoot` | CC/sandboxing; P §3 |
| Bash write to `/var/folders/.../archwright-<uid>` | No (OS-denied) | outside sandbox write set, #75 | P §3 |
| Bash write outside repo/tmp (`~/x`) | No | sandbox OS-denied; `~` redirect prompts | CC/permissions; RS |
| `curl https://github.com/...` | Rule yes; network yes (allowedDomains) | managed `curl *`; sandbox proxy allows github.com | RS |
| `curl https://example.com` | No | domain not in allowedDomains (UNVERIFIED unless checked) | RS |
| WebFetch github.com | No | no allow rule; not preapproved (UNVERIFIED) | CC/tools-reference |
| `npm test`, `npm run x`, `node` | Yes | managed `npm *`, `node *`; sandboxed, local only | RS |
| `npm install` | Rule yes; network no | no npm registry in allowedDomains | RS |
| `pnpm test`, `go test`, `make` | No | no rule, not read-only | RS |
| `FOO=bar npm test` | No | unknown env var not stripped; allow rule won't match | CC/permissions#wrappers |
| `sudo ...`, `Read ~/.ssh/**` | No (denied) | managed deny | RS |
| Unsandboxed retry | No | dontAsk denies | CC/sandboxing |
| MCP tools injected by Archwright | No | `allowManagedMcpServersOnly` | RS; ADR 0014 |

## 4. Codex: local policy found

- `~/.codex/config.toml`: `[features] hooks = true`, TUI flags, hook trust hashes, per-project `trust_level = "trusted"` entries. No `approval_policy`, `sandbox_mode`, `network_access`, `writable_roots` (also A header: none present).
- `~/.codex/rules/default.rules`: single line `prefix_rule(pattern=["archwright", "tool"], decision="allow")` (read this session).
- No `/etc/codex/`, `requirements.toml`, `managed_config.toml` (A finding 2; `/etc/codex` absent, checked). Managed policy is cloud-delivered: `~/.codex/cloud-config-bundle-cache.json` (signed; payload not decoded here). Observed effects (A finding 2-3): `exec` sets `approval_policy=Never`, managed requirements force `OnRequest` (allowed set `[OnRequest, UnlessTrusted]`); non-fatal error items; exit 0. Allowed sandbox modes, `[rules]`, network permission: not read, UNVERIFIED.
- `codex exec` defaults: read-only sandbox (C2 §2, NI); `-s workspace-write` for writes. Needs git dir or `--skip-git-repo-check` (P §4; doctor probe `git init`s, #66/#68).
- Escalation: "command execution approval is not supported in exec mode": request fails, exit 0 (A finding 5). Sandbox denial = plain command failure (A finding 4). `--approve-for-me` works but forbidden by ADR 0011.
- Workspace-write: cwd + temp dirs writable; `.git`, `.agents`, `.codex`, `.aws` read-only recursively (CX/agent-approvals-security; C2 §2). Network off unless `[sandbox_workspace_write] network_access = true` (same). Observed: loopback HTTP worked, unix sockets failed (P §3; docs/research/works-endpoint-reachability-from-runner-sandboxes.md).
- `on-request`: "commands allowed by the sandbox can run without approval, read accessible files, and use network access if enabled" (CX/agent-approvals-security). So under on-request, inside-sandbox commands run with no prompt, without needing a known-safe list; the list only matters for `untrusted`/`unless-trusted`, which `exec` cannot select (A finding 3: `untrusted` rejected as no longer supported in config).
- Known-safe list: source file not located (`codex-rs/shell-command/src/command_safety/` has `is_dangerous_command.rs` (blocklist: `rm -f/-rf`, wrappers `sudo`/`env`/`trap`; no allowlist) and Windows files; `is_safe_command.rs` raw URLs 404 at tried paths) (github.com/openai/codex tree, WebFetch). From memory, UNVERIFIED: safe = `ls, cat, head, tail, pwd, echo, grep, rg (no --pre), wc, which, git status|log|diff|show|branch, find (no -exec/-delete), sed -n`.
- execpolicy rules: `prefix_rule(pattern=[...], decision="allow|prompt|forbidden")` in `~/.codex/rules/*.rules`; `--ignore-rules` skips them (C2 §1). Whether an `allow` rule also bypasses the sandbox (vs only skipping the approval) UNVERIFIED; rules doc URL 404 (`CX/rules`). Whether `[rules]` from managed requirements restrict it: UNVERIFIED.

## 5. Codex table (headless `codex exec -s workspace-write`, managed `OnRequest`)

| Action | No prompt? | Why | Source |
|---|---|---|---|
| `git status/log/diff`, `ls`, `cat`, `grep`/`rg` | Yes (runs) | read inside sandbox; no approval needed | CX/agent-approvals-security |
| `gh issue view` | Network off: fails by default; auth/TLS UNVERIFIED | workspace-write network off; escalation unsupported in exec | CX; A finding 5 |
| `archwright tool x` | Allowed by prefix_rule; works only if sandbox lets it write spool | local `default.rules`; spool under temp OK; Codex runs sandboxed | `~/.codex/rules/default.rules`; UNVERIFIED sandbox bypass |
| Piped / compound commands | Yes if sandbox allows each | approval not per-subcommand when sandbox permits | CX (inference) |
| Write in repo (cwd) | Yes (workspace-write); No on `-s read-only` | writable root | CX; C2 |
| Write `.git/*`, `.agents/`, `.codex/` | No (fails) | protected read-only | CX; C2 §2 |
| Write to spool root `/tmp/claude-<uid>/archwright` | Yes (temp dirs writable) UNVERIFIED path; `/private/tmp` write succeeded | workspace-write allows temp | A finding 4; ADR 0018 |
| Write outside workspace | No (fails, not prompt) | sandbox denial | A finding 4 |
| Network `curl github.com` | No | network off; escalation unsupported | A finding 5 |
| `npm test` / `pnpm test` | Yes if local only | sandboxed local exec | CX (inference) |
| `npm install` | No | network off | CX |
| Escalated command | No (fails, exit 0) | no approvals in exec | A finding 5 |
| MCP injected by Archwright | Restricted by managed `mcp_servers` UNVERIFIED | CX/enterprise/managed-configuration | C2 §3 |
| Non-git dir | Refused | needs git or `--skip-git-repo-check` | P §4 |

## 6. Side by side

| Aspect | Claude Code (dontAsk) | Codex exec (workspace-write) |
|---|---|---|
| Who decides | Allow rules (managed only) + built-in read-only set + sandbox | Sandbox only; approvals impossible |
| Unlisted command | Denied (permission_denied event) | Runs if sandbox permits, else plain failure |
| Read-only commands | Named built-in set; compound requires all parts | Anything the sandbox can read (UNVERIFIED rg/find limits) |
| Repo writes | Edit/Write tool (managed allow) + Bash within sandbox | Bash/patch in cwd, `.git`/`.agents`/`.codex` protected |
| Spool root | `/tmp/claude-<uid>/archwright` writable | temp dirs writable; confirm path UNVERIFIED |
| Network | Proxy allowlist (github.com yes, npm no); gh TLS broken (#73) | Off by default; no escalation |
| `archwright tool` | Needs managed allow; user rule ignored | prefix_rule exists locally; sandbox bypass UNVERIFIED |
| Escalation | Retry denied under dontAsk | Unsupported in exec |
| User-config override | Blocked for permission rules | `--ignore-user-config`/`--ignore-rules` exist; managed requirements enforced |

## 7. Implications for Warden/Wright prompts and tool design

- Prompts: tell agents to use read-only built-ins, one command at a time or pure read-only pipes; no `cd` + `git`, no `FOO=bar cmd`, no trailing `&&`, no `bash -c`/`npx`, no unquoted globs with `find`/`sed`/`git`. Prefer Edit/Write tools for repo files over shell redirects.
- Tell agents `gh` will likely fail in the sandbox (Claude TLS #73; Codex network off). Provide issue/PR text in the Brief (Works-brokered fetch, P option 1); do not tell agents to `gh issue view N`.
- `archwright tool` is the only sanctioned side channel but is denied under Claude `dontAsk` unless admins add a managed allow (P Open Q1). Verify with a live dontAsk run before relying on it; doctor should probe under dontAsk and report the managed-rule gap.
- Write only to repo and the runner-specific spool root (`launch.SpoolRoot`); never `os.TempDir()` (#75, PR #77). Codex: do not write `.git`, `.agents`, `.codex`.
- Test commands: `npm test`/`node` fine on Claude; `pnpm`, `go test`, `make` are not allowed (no managed rule) so denied; offer an `archwright tool` wrapper for test runs or ask admins to allow. Dependency installs fail (no registry domain, Codex no network).
- Do not rely on classifier (`auto`) behavior; Archwright runs `dontAsk` (ADR 0011).
- Doctor should surface: effective Claude allow gaps (`archwright`, `pnpm`, `go`), `gh` TLS, Codex `OnRequest` override and network state; remedies are developer/admin actions, never written by Archwright.
- Open: live-test (1) `gh issue view 75` vs managed exact rule, (2) `rg`, (3) Codex prefix_rule bypass, (4) Codex safe list from source, (5) Codex spool path writability, (6) decode cloud bundle (`allowed_sandbox_modes`, `[rules]`).
