# Headless claude/codex under company-managed settings

Researched 2026-11-17. Sources: official docs (WebFetch summaries, so re-verify exact wording before depending on it), local CLIs (`claude 2.1.285`, `codex-cli 0.162.1`), repo files. Anything not confirmed in a primary source is marked UNVERIFIED. Builds on earlier notes in `docs/research/` (claude-code-headless-and-managed-settings.md, codex-headless-approvals-test.md, works-endpoint-reachability-from-runner-sandboxes.md); not repeated in full.

Doc base: `CC/` = https://code.claude.com/docs/en/ ; `CX/` = https://learn.chatgpt.com/docs/ (OpenAI Codex docs; developers.openai.com/codex redirects here).

## 0. This machine (facts, no secrets)

- No file-based policy: `/Library/Application Support/ClaudeCode/` absent (also `docs/research/managed-policy-mcp-and-hooks-check.md`). Policy is server-managed: `~/.claude/remote-settings.json` (+ signature files), `~/.claude/policy-limits.json` (feature restrictions, e.g. remote sessions off).
- `remote-settings.json` top-level keys: `allowManagedPermissionRulesOnly: true`, `allowManagedMcpServersOnly`, `allowedMcpServers`, `deniedMcpServers`, `permissions{allow,deny}`, `sandbox`, `autoMode`, `disableAllHooks`, `env`, others.
  - `permissions.allow` ~105 entries (read-only `gh issue view`, `gh pr view`, `git diff`...); no `archwright` rule. `deny` ~31 (secrets reads, `Bash(sudo *)`, some MCP writes).
  - `sandbox.enabled: true`; `filesystem.allowWrite` = only 3 app paths (`~/Library/Application Support/rtk`, Secretive ssh socket, `~/.jira`); `network.allowManagedDomainsOnly: false`; `allowedDomains` ~175 incl. `github.com`, `*.github.com`, `*.githubusercontent.com`; `allowUnixSockets` = Secretive socket. No `excludedCommands`, no `allowUnsandboxedCommands` key seen.
- User `~/.claude/settings.json` has `permissions.defaultMode: auto` and `allow: ["Bash(archwright tool:*)"]`; no `sandbox` key.
- **Tension (UNVERIFIED cause):** `allowManagedPermissionRulesOnly: true` means user `allow` rules are ignored (CC/settings-reference#allowmanagedpermissionrulesonly, quoted below), yet issue #75 says the `Bash(archwright tool:*)` rule "works". Likely the auto-mode classifier, not the rule, approved it. Needs a test (see Open questions).

## 1. Claude Code managed settings

### Precedence and non-overridability
- Order, high to low: managed, `--settings`, project local, project shared, user. "Nothing in your own settings files or `--settings` overrides a managed key." CC/settings#settings-precedence
- Lists (e.g. `permissions.allow`) merge across scopes. CC/settings#lists-merge-instead-of-overriding
- Deny wins at any level: "a managed settings deny can't be overridden by `--allowedTools`, and `--disallowedTools` can add restrictions". CC/permissions#settings-precedence
- `allowManagedPermissionRulesOnly`: "Claude Code then ignores `allow`, `ask`, and `deny` rules in user, project, local, and `--settings` files, ignores `--allowedTools`, hides the always-allow choices... and stops saving new rules." CC/settings-reference#allowmanagedpermissionrulesonly. Note: `--disallowedTools` is not listed as ignored (inference; UNVERIFIED).
- Managed-only lock: `sandbox.network.allowManagedDomainsOnly` (Scope: Managed). `sandbox.filesystem.allowWrite`, `sandbox.excludedCommands`, `sandbox.allowUnsandboxedCommands` are scope "Any file". CC/settings-reference
  - So where `allowManagedDomainsOnly` is false (this machine) and no admin-required lock is set, user/`--settings` sandbox keys can add `allowWrite`, `allowedDomains`, `excludedCommands` (merged, not replaced: CC/sandboxing "combining the paths"). Whether repo/user can still add to an admin-set `allowedDomains` when `allowManagedDomainsOnly` is false: merge implied, UNVERIFIED for this tenant.
  - Per earlier note: when managed sets `allowUnsandboxedCommands:false` or `allowManagedDomainsOnly:true`, sandbox is "admin-required" and lower scopes cannot override the listed keys (docs/research/claude-code-headless-and-managed-settings.md, citing CC/sandboxing).
- Exceptions where lower scopes can only tighten (e.g. `permissions.denyInsteadOfPrompting` honored `true` from user or `--settings`): CC/settings#exceptions-to-managed-settings-precedence.
- `permissions.defaultMode` `auto`/`bypassPermissions` ignored from project/local; allowed from user, managed, or `--permission-mode`. CC/settings#a-value-you-set-is-ignored
- Managed settings apply to `-p` and SDK sessions regardless of `settingSources`. CC/managed-settings (earlier note). Unparseable managed doc: `claude -p` refuses to start. CC/errors#managed-settings-document-could-not-be-parsed
- `-p` skips a broken user/project/local file and continues; `claude doctor` shows what was dropped. CC/settings#fix-a-broken-settings-file

### What a headless run can/cannot override
| Lever | Effect under managed | Source |
|---|---|---|
| `--settings` | Above user/project, below managed. Can set `sandbox.*` (scope Any), but permission rules ignored when `allowManagedPermissionRulesOnly` | CC/settings#change-a-setting-for-one-session |
| `--allowedTools` | Ignored when `allowManagedPermissionRulesOnly`; never beats a managed deny | CC/settings-reference, CC/permissions |
| `--disallowedTools` | Adds restrictions only | CC/permissions |
| `--permission-mode` | Valid modes via `claude --help`: acceptEdits, auto, bypassPermissions, manual, dontAsk, plan. Managed `disableBypassPermissionsMode` blocks bypass (startup behavior UNVERIFIED) | local `claude --help`; CC/permissions |
| `--dangerously-skip-permissions` | Bypass; managed denies still hold; managed `disableBypassPermissionsMode` can remove it | CC/permissions; agent-sdk/permissions (earlier note) |
| `--permission-prompts none\|host` | `none`: anything that would prompt is denied; mode still decides rest | local `claude --help` |
| env vars | Not a precedence level; per-key pairing (CC/settings#settings-precedence). A managed `env` block is ordinary managed setting | CC/settings |

Archwright already forbids all of these in `internal/runner/guard.go` (never-list; ADR 0011).

## 2. Headless permission denials

- `-p` with nothing approving: a call that needs approval is denied unless approved by allow rule, `PermissionRequest` hook, `--permission-prompt-tool`, or SDK `canUseTool` (docs/research/claude-code-headless-and-managed-settings.md §2, citing CC/headless, agent-sdk/permissions). No TTY prompt appears.
- Denials surface as `permission_denied` events and `permission_denials` in the final stream-json result (same note). Archwright normalizes these (`internal/runner/runner.go` `KindPermissionDenied`).
- `--permission-prompt-tool mcp__server__tool`: an MCP tool answers prompts. Blocked in practice here: injected MCP servers are refused by enterprise policy (docs/research/managed-policy-mcp-and-hooks-check.md; `allowManagedMcpServersOnly: true` in remote settings). Also ADR 0011 rejects a live approval relay.
- `PermissionRequest` hook can allow; `disableAllHooks` is present in remote settings (value not inspected beyond key existence) so hook-based approval is UNVERIFIED viable; earlier test showed `--settings` hooks do run.
- `auto` mode (this user's default) uses a classifier model to decide instead of a human (CC/permissions). Headless behavior when the classifier blocks repeatedly: UNVERIFIED.
- Unsandboxed retry (`dangerouslyDisableSandbox`) gating, CC/sandboxing#the-unsandboxed-retry-escape-hatch:
  - `allowUnsandboxedCommands:false`: parameter ignored, retry refused.
  - `dontAsk`: "Claude Code denies the retry". Archwright's doctor probe and (per #75) step runs use dontAsk, so the retry is always denied (matches #73/#75).
  - Manual: prompt (denied headless). Auto: classifier. A matching allow rule such as `Bash(curl *)` approves the retry; an ask rule `Bash(dangerouslyDisableSandbox:true)` forces approval.
  - Under `allowManagedPermissionRulesOnly` the developer cannot add that allow rule; only admins can.

## 3. Sandbox specifics

- Default writable: cwd (+subdirs), the per-user temp dir, and `--add-dir`/`additionalDirectories` dirs. "Claude Code sets `$TMPDIR` to this directory for sandboxed commands." CC/sandboxing
  - Sandbox shell shows `/tmp/claude` and `/private/tmp/claude` writable in this session; per-user temp is a `/tmp/claude-<uid>` area. ADR 0018 and `launch.SpoolRoot` encode this (`/tmp/claude-<uid>/archwright`).
  - A `/var/folders/.../T/archwright-<uid>` root (os.TempDir under the developer's shell) is NOT writable: #75.
- `allowWrite` is OS-enforced, merges across scopes. On Linux a nonexistent path has no effect. CC/sandboxing
- Network: proxy runs outside the sandbox; commands pointed at it via `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY`; "allowed domains start empty" (managed list supplies them here). CC/sandboxing
- Unix sockets and loopback to the Works fail inside the claude sandbox (docs/research/works-endpoint-reachability-from-runner-sandboxes.md, empirical; cause not isolated). Only `allowUnixSockets` entries work (Secretive socket here).
- **#73 gh TLS:** CC/sandboxing: "Go-based CLIs fail TLS verification on macOS: Tools such as `gh`, `gcloud`, and `terraform` may fail TLS verification under Seatbelt." Fixes the docs give: (a) `sandbox.excludedCommands: ["gh *"]` (then gh "runs with your full access and its stored credentials"); (b) for MITM proxy + custom CA, `sandbox.enableWeakerNetworkIsolation: true` (let gh verify TLS via the macOS trust service). Issue text: `x509: OSStatus -26276` (gh issue 73). `github.com` is already in `allowedDomains` here, so the failure is TLS trust, not allowlist. Whether `enableWeakerNetworkIsolation` alone fixes plain (non-MITM) gh here: UNVERIFIED; the doc ties it to the MITM case.
  - Both keys are scope "Any file", so a developer could set them in `~/.claude/settings.json`, unless the admin locks sandbox (not locked here). Archwright must not write them (ADR 0011); doctor can only tell the developer.
  - `excludedCommands` matching: Bash-rule syntax, `gh *`; every command in a compound call must match; "a script that calls docker internally doesn't match". CC/sandboxing
- **#75 / PR #77:** root cause: step Wardens, Wrights, review used `Runs.spoolRoot` = `os.TempDir()/archwright-<uid>`, only the interactive launch used `launch.SpoolRoot` (gh issue 75; `internal/works/runs.go:90`). PR #77 (open, 5 files in `internal/works`) adds `spoolRootFor(guild)` resolving the Guild's bound runner to `launch.SpoolRoot`, fails the run if the Guild lookup fails, keeps `SetSpoolRoot` test override. PR body: no live claude/codex run done (live validation inconclusive, 0/3). Doctor's claude probe already used `launch.SpoolRoot` so stayed green (`internal/doctor/probe.go`).
- Sandbox wraps Bash only; not Read/Edit/Write, WebFetch, MCP, hooks (earlier note). So brokering through file tools or WebFetch evades the Seatbelt but not permission rules.

## 4. Codex equivalents

| Concern | Codex | Source |
|---|---|---|
| Sandbox modes | `read-only`, `workspace-write`, `danger-full-access`; macOS Seatbelt, Linux bwrap+seccomp | CX/agent-approvals-security; `codex exec --help` |
| Approval | `on-request`, `auto_review`, `never`, granular. `exec` itself sets `approval_policy=Never`; managed requirements forced `OnRequest` here | CX/agent-approvals-security; docs/research/codex-headless-approvals-test.md (empirical) |
| Escalation headless | "command execution approval is not supported in exec mode": command fails, exit 0 | approvals-test finding 5 |
| `--approve-for-me` | Works headless via model reviewer; forbidden by ADR 0011 | approvals-test finding 6; `codex exec --help` |
| Network | off in workspace-write unless `[sandbox_workspace_write] network_access = true`; optional `features.network_proxy` domain rules; deny wins | CX/agent-approvals-security |
| Observed | loopback HTTP worked under workspace-write (surprising); unix socket failed | works-endpoint note (empirical) |
| Writable | cwd + `/tmp`/temp dirs; `.git`, `.agents`, `.codex`, `.aws` read-only inside writable roots | CX/agent-approvals-security |
| Git dir | `codex exec` refuses non-git dirs unless `--skip-git-repo-check`; doctor probe `git init`s (#66/#68) | `codex exec --help` (flag exists); `internal/doctor/probe.go` |
| Managed enforcement | `requirements.toml` (enforced: `allowed_approval_policies`, `allowed_sandbox_modes`, `allowed_permission_profiles`, `mcp_servers`...), `managed_config.toml` (defaults only, user-changeable). On conflict client "falls back to a compatible value". Cloud-delivered requirements outrank local | CX/enterprise/managed-configuration |
| This machine | cloud bundle cache `~/.codex/cloud-config-bundle-cache.json`; `Never` replaced with `OnRequest` plus non-fatal error items, exit 0 | approvals-test finding 2 |
| Flags | `-s/--sandbox`, `-c key=value`, `--ignore-user-config`, `--ignore-rules`, `--skip-git-repo-check`, `--ephemeral` | `codex exec --help` |

- Local requirements file paths (`/etc/codex/requirements.toml` or similar): UNVERIFIED; the fetched doc did not state them (CX/enterprise/managed-configuration).
- Codex has no equivalent of the `dangerouslyDisableSandbox` retry; the only escalation is an approval request, which plain `exec` cannot grant. Network/gh from a Codex Warden: needs `network_access = true` in the developer's config (not set by Archwright; ADR 0011 never-list covers `-c`); whether managed requirements permit it: UNVERIFIED. Whether gh TLS also fails under Codex Seatbelt: UNVERIFIED (issue #73 notes Wrights likely same, unverified).

## 5. Design options and doctor preflight (context)

Repo facts: ADR 0011 (never widen permissions, no settings writes, dontAsk, never-list in `internal/runner/guard.go`); ADR 0014 (tools reach agents via `archwright tool` CLI over Bash, since injected MCP is refused); ADR 0018 (file spool under each runner's writable temp, Works watches it); doctor probes each runner by running `archwright tool doctor_ping` inside the runner (`internal/doctor/probe.go`). Doctor probe only tests spool + permission rule, not gh/TLS, not the real spool root of real runs (before #77).

## What this means for Archwright

Ranked options. The agent's sandboxed shell cannot reach the network or full-access gh, but the Works (the daemon) is a normal developer-user process, outside the runner sandbox.

1. **Broker data-fetching through the Works, not the agent (best fit for #73/#76).** The Works (or Archwright at Job creation, which is interactive) fetches the issue text with the developer's own gh and puts it in the Job description/Brief; agents never call gh. Fits ADR 0011/0014/0018 with no policy change; works identically for claude and codex. Cost: Works holds GitHub credentials/rate limits; ADR needed for "Works may call GitHub on the developer's behalf" (matches #73 decision list). Extend to other privileged reads (PR open/comment are already Warden/Works actions per ADR 0008).
2. **Brokered write actions as spool tools.** Generalize: any action needing network/unsandboxed access (open PR, post comment, fetch URL) is a role-authorized `archwright tool <name>` the Works executes outside the sandbox. Already the pattern (ADR 0014/0018); authorization by role + token. Keeps `allowUnsandboxedCommands` irrelevant. Cost: tool surface grows; each is an injection-reachable capability, so role-scope them.
3. **Preflight expansion in `archwright doctor`.** Make doctor probe what real runs need and print the exact developer remedy without writing it: (a) probe the same spool root real runs use (#77); (b) detect `/status`-equivalent facts: effective permission mode, whether `Bash(archwright tool:*)` is honored vs classifier-approved (probe under `dontAsk` as now; if the rule is ignored by `allowManagedPermissionRulesOnly`, the probe is denied and doctor must say the rule cannot come from user settings and the admins must allowlist it or the developer must use a mode where it passes); (c) if any step brief tells agents to run `gh`, probe a harmless `gh` call and report the `excludedCommands: ["gh *"]` / `enableWeakerNetworkIsolation` remedies as developer-edited, optional config; (d) for codex, probe inside a git dir (already) and report requirements-forced `OnRequest` and network status. Cheap, no policy change.
4. **Developer-side remedies documented, not applied.** `sandbox.excludedCommands: ["gh *"]` or `allowWrite` in `~/.claude/settings.json` (scope Any file, unlocked here). Works only if admin has not locked sandbox, and gh then runs with full access (CC/sandboxing warning). Weakest for a fail-closed factory; keep as doctor hint only.
5. **Not viable / forbidden:** `--dangerously-skip-permissions`, `bypassPermissions`, `--approve-for-me`, writing settings, `--permission-prompt-tool` via injected MCP (blocked), `--settings` that overrides managed keys (never-list; ineffective on managed keys anyway). Setting `denyInsteadOfPrompting` via `--settings` is permitted by docs as tightening, but dontAsk already does this.

## Open questions for /grill-with-docs

1. Is `Bash(archwright tool:*)` actually honored here, or is the auto classifier approving it? `allowManagedPermissionRulesOnly: true` says user rules are ignored. Test a Warden run in `dontAsk` (current step mode) vs `auto`, and ask admins to allowlist `archwright tool`?
2. Which fix owns #73: Works/Archwright fetches issue text (option 1), or doctor-guided `excludedCommands: ["gh *"]`? Does the Works get an ADR for calling GitHub with the developer's credentials, and what about non-GitHub trackers?
3. Should step Wardens run in `dontAsk` (denies the retry, matches ADR 0011) or the developer's own default (`auto` here, classifier-gated)? ADR 0011 says "no more permissive than the runner's default"; is the default `auto` or `default`?
4. Should doctor probe the real runs' spool root and posture (post-#77), and fail visibly when the probe's root differs from what `Runs` will use?
5. Does `sandbox.enableWeakerNetworkIsolation` alone fix `gh` TLS here (non-MITM)? Needs a live test; if not, only `excludedCommands` or Works-brokered fetch work.
6. For Codex: does the cloud requirements bundle allow `network_access = true` and `workspace-write`? Does gh TLS fail under Codex Seatbelt too? Where would a local `requirements.toml` live?
7. Is a broker tool (`archwright tool gh_issue_view`) with a role-scoped allowlist acceptable as the general escape from sandbox limits, or too wide a prompt-injection surface?
8. PR #77 lacks live validation; should a live fixture run (W01) gate merging?
