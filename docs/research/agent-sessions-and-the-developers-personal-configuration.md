# What does an agent Session load from the developer's personal configuration, and can it be isolated? (#161)

Researched 2026-11-26 on Linux amd64 with `claude` 2.1.332 and `codex-cli` 0.169.0, against `main` at b20acce. Primary sources: Claude Code docs (code.claude.com/docs), Codex docs (developers.openai.com/codex, which now redirects to learn.chatgpt.com/docs), the CLIs' own `--help` and output, and this repo's ADRs and code. WebFetch returns small-model summaries of long pages, so the quotes below were re-checked against the page text where I could; the wording is theirs, the page can change. Anything I could not measure is marked UNVERIFIED with the reason. No token, credential, account id or email appears here.

## Conclusion

- Recommend a variant of option 2, narrower than the issue's option 4: for Claude, pass `--setting-sources project,local` and put `"disableClaudeAiConnectors": true` in the Works' own hooks settings file. Do not use `CLAUDE_CONFIG_DIR` (option 3): it breaks login, and with it every server-managed policy. Do not use `--strict-mcp-config` as the main MCP lever: the docs say it exits when an enterprise `managed-mcp.json` is present, and the settings key does the same job without a new flag.
- Measured here (headless proxy, see Method): the two changes together took a Session's skills from 63 to 19 (the 19 are Claude's bundled and built-in plugin skills plus the delivered project skill, none of them the developer's), removed the developer's user plugin and its hooks (6 SessionStart hook starts down to 1, the Works' own), and removed the connected claude.ai MCP server. The Works' hooks still fired, the delivered Guild skill still loaded, and login still worked.
- The price is real and the maintainer should weigh it: the developer's user-level permission rules (including the one ADR 0014 asks them to add for `archwright tool`) stop applying, and the same for their user-level default mode, `env` and model settings. That is not measured here (UNVERIFIED), it follows from the documented scope of `--setting-sources`.
- Whether managed policy still applies under each option is documented, not tested: this Linux machine has no managed policy, so I could not make a managed denial happen. See "Managed policy".
- For Codex there is no equivalent switch for an interactive Session. `--ignore-user-config` exists only on `codex exec`. The Works already owns a `CODEX_HOME` for Codex Sessions, so isolation there is a change to what the Works links into it, not a new flag. The developer's `~/.agents/skills` is read regardless of `CODEX_HOME` (measured).

## Stale premises in the issue

The issue was written for headless `claude -p` runs. These parts no longer hold.

1. **Headless is gone.** ADR 0003 ("stock CLI subprocesses, `claude -p`, `codex exec --json`") is superseded by ADR 0026: every agent is an interactive Session of the stock CLI in a tmux pane. There is no init event and no `/runs/{id}/events/{seq}/raw` stream-json to read. The issue's recipe for seeing what a run loaded no longer applies to Sessions.
2. **ADR 0011 is superseded by ADR 0027.** Permissions are spawn rules; the developer answers prompts in the pane. "Does a command the managed allow list denies stay denied" is now "does the prompt or deny still happen", and the Works never approves anything.
3. **ADR 0022 and ADR 0024 are superseded by ADR 0028.** The Works no longer holds credentials and brokers every Forge action; agents run `gh` and `git push` themselves, gated only by the pane prompt. So the issue's argument "a Wright could call Jira and Bitbucket as the developer, which sits badly with ADR 0022" no longer rests on ADR 0022. The remaining argument is reproducibility, dilution of the Guild's skills, and personal tools acting outside the factory's remit. It is weaker for the Forge (agents may act as the developer by design) and unchanged for non-Forge MCP servers.
4. **ADR 0014's hook statement is stale in the other direction.** It says hooks come through `--settings` "under the observed policy". Hooks now drive all state tracking (`docs/spec/tmux-runtime.md`), so isolation must not remove them.
5. **The guard list is mixed.** `internal/runner/guard.go` `forbiddenFlags` still lists `-p` and `--bare`, which no longer matter for Sessions, and lists `--settings`, `--setting-sources`, `--mcp-config`, `--strict-mcp-config`, `-c`, `--config`, `--profile`, `--ignore-user-config`, `--enable` and `--disable` as refused in `Spawn.ExtraArgs`. The Claude adapter itself passes `--settings <hooks.json>` (`internal/session/vendor.go`, `claudePlan`), so the guard only refuses a caller-supplied one, and the Works' own is already in use.
6. **The issue's "what is true today" numbers came from a developer's Mac** (65 skills, ten MCP servers, four plugins, 103 tools). Mine are from this Linux machine and differ (63 skills, one connected server, six plugins counting built-ins, 32 tools). The shape is the same.
7. **`--ignore-user-config` is not a flag of interactive Codex.** The issue and the guard treat it as a Codex runner flag. In 0.169.0 only `codex exec --help` lists it; `codex --help` does not.
8. **Option 3 was not as the issue assumed.** The issue said an isolated config dir "may also change how auth works". It does: see below.

### Re-framed question

For an interactive Session of the stock CLI, in a worktree the Works created, with hooks the Works supplies: what does the Session load from the developer's user-level configuration (skills, plugins, hooks, MCP servers, agents, permission rules, settings), which of that is wanted, and which lever removes the unwanted part without removing (a) managed policy, (b) the developer's login, (c) the Works' hooks, or (d) the delivered project skills?

## Method and what it can and cannot show

All commands are in "Commands used". Scratch directory: `$S` = `/tmp/claude-1000/-home-phillip-Development-archwright/7e0d3a43-2740-48cf-b6ef-c2e271c03616/scratchpad`. The scratch project `$S/proj` is a fresh `git init` with one skill, `guild-probe`, written to `.claude/skills/guild-probe/SKILL.md` and `.agents/skills/guild-probe/SKILL.md` as `internal/skills/deliver.go` does (`.agents/skills`, plus `.claude/skills` for Claude). Prompt: `Reply with OK`, no tools. `$S/works-hooks.json` stands in for the Works' hooks file: a `SessionStart` and a `UserPromptSubmit` command hook that append a line to a marker file in `$S`. Nothing was written to `~/.claude`, `~/.codex` or any settings file by me, and no MCP tool was called.

- **Claude: headless proxy.** An interactive Session has no init event. I ran `claude -p --output-format stream-json --verbose --include-hook-events` and read the `system`/`init` event and the `hook_started` events. The loader for skills, plugins, hooks, MCP and settings sources is the same code path as an interactive start, but I did not run an interactive Session (it would need a tty and a trust answer, and trust is written to `~/.claude.json`). So what differs for interactive is UNVERIFIED: the trust dialog and project-scope MCP approval prompts (docs: `-p` "loads project-scoped servers without asking"; interactive prompts first).
- **Codex: `codex debug prompt-input`** renders the model-visible prompt (including the skills list and roots) without a model call. It does not show MCP servers, hooks or plugins, and it takes no `--ignore-user-config`. I also ran `codex exec` once with and once without `--ignore-user-config`, `--ephemeral`, to check auth.
- **Machine facts.** No managed policy file here (`/etc/claude-code`, `/etc/codex` do not exist). Whether server-managed settings apply to this login I did not read (that needs `~/.claude`, outside what I may touch). The developer's Claude config here has one connected MCP server, a claude.ai connector (`claude.ai Claude Docs`, `source: claudeai`), no user-scope server of its own, and one user plugin (`gitkraken-hooks`) with hooks. The developer's `~/.claude/skills` has 33 entries. Codex has no MCP servers configured (`codex mcp list`), a `hooks.json` in its home and many per-project trust entries in `config.toml`.

## Claude: measurement matrix

Every row: `claude -p "Reply with OK"` in `$S/proj` with `--settings $S/works-hooks.json` (the Works' hooks), `--output-format stream-json --verbose --include-hook-events`, plus the flags shown. `apiKeySource: none`, `permissionMode: default` in every cell that ran. "Hooks started" counts `hook_started` events for SessionStart / UserPromptSubmit (the Works' own hook is one of each).

| Cell | Flags / env | Skills (guild-probe loaded) | MCP servers (status) | Plugins | Agents | Tools | Hooks started (S/U) | Works hook fired | Auth |
|---|---|---|---|---|---|---|---|---|---|
| a. today | none | 63 (yes) | `claude.ai Claude Docs` connected | gitkraken-hooks (user) + 5 built-in | 6 | 32 | 6 / 2 | yes | ok |
| b. | `--setting-sources project,local` | 19 (yes) | `claude.ai Claude Docs` connected | 4 built-in | 6 | 32 | 1 / 1 | yes | ok |
| c. | `--strict-mcp-config`, no `--mcp-config` | 63 (yes) | none | gitkraken-hooks + 4 built-in | 6 | 24 | 6 / 2 | yes | ok |
| d. | `CLAUDE_CONFIG_DIR=<empty dir>` | 19 (yes) | none | 4 built-in | 5 | 23 | 1 / 1 | yes | **failed: `Not logged in`, exit 1. Auth did not carry over; cell stopped.** |
| e. b + c | `--setting-sources project,local --strict-mcp-config` | 19 (yes) | none | 4 built-in | 6 | 24 | 1 / 1 | yes | ok |

Reading the cells:

- **a.** Skills: 63 = the developer's `~/.claude/skills`, the Guild-style project skill, bundled skills, plugin skills and claude.ai-synced skills. The Guild would deliver 1 to 7. Hooks: 5 non-Works SessionStart hooks and 1 non-Works UserPromptSubmit hook run beside the Works' (from the user plugin and user settings; names are not in the event stream, so I cannot say which).
- **b.** Drops everything from `~/.claude`: user skills, user plugin, user hooks, user agents, user permission rules. Keeps built-in plugins, bundled skills, the delivered project skill and the `--settings` hooks. The claude.ai connector stays: it comes from the login, not from a settings file. The tool count is unchanged because that connector's tool stays.
- **c.** Removes the MCP server and its tool (32 to 24) and nothing else. Skills, plugin and hooks untouched.
- **d.** An empty `CLAUDE_CONFIG_DIR` gets the isolated skills/plugins/hooks of b, and no login. The docs say why this is structural: "All settings, session history, and plugins are stored under this path" and credentials are located by it (`CLAUDE_CONFIG_DIR` entry in the env-vars page; credential location is on the authentication page, not read in full here). I did not copy, link or point at any credential file to make it work (that is outside my safety rules, and it would move the developer's login into a place the factory chose). Whether macOS Keychain lookup is keyed by the config dir: UNVERIFIED, I could not test on a Mac.
- **e.** Equals b plus c. Because b already carries the connector, c is what removes it here.

### Extra cells I ran

These are not in the issue's matrix but bear on the recommendation.

| Cell | Flags / env | Skills (guild-probe) | MCP | Plugins | Tools | Hooks started (S/U) | Works hook fired | Auth |
|---|---|---|---|---|---|---|---|---|
| g1. | `disableClaudeAiConnectors: true` added to the Works' `--settings` file | 63 (yes) | none | gitkraken-hooks + 4 built-in | 24 | 6 / 2 | yes | ok |
| g2. | `ENABLE_CLAUDEAI_MCP_SERVERS=false` | 63 (yes) | none | gitkraken-hooks + 4 built-in | 24 | 6 / 2 | yes | ok |
| g3. (**recommended**) | b + `disableClaudeAiConnectors: true` in the Works' settings file | 19 (yes) | none | 4 built-in | 24 | 1 / 1 | yes | ok |
| f1. | `--safe-mode` | 18 (**no**) | none | gitkraken-hooks + 4 built-in | 24 | **0 / 0** | **no** | ok |
| f2. | `--restricted` | 18 (**no**) | none | 4 built-in | 22 | 1 / 1 | yes | ok |
| f3. | `--disable-slash-commands` | **0** (no) | `claude.ai Claude Docs` connected | gitkraken-hooks + 4 built-in | 31 | 6 / 2 | yes | ok |
| f4. | `--bare` | 19 (no) | none | listed | 3 | 0 / 0 | no | **failed: `Not logged in`, exit 1** (OAuth and keychain are never read under `--bare`) |
| f5. | b + `--strict-mcp-config` + `--disable-slash-commands` | 0 (no) | none | 4 built-in | 23 | 1 / 1 | yes | ok |

- **g1 and g3 settle the MCP question without a flag.** `disableClaudeAiConnectors` is a settings key the docs list among the keys honoured when `true` "from any scope", "even when a managed source sets `false`". The Works already hands Claude a settings file through `--settings`; the key goes in it.
- **g2** shows the environment variable also works, but the Works' environment allowlist and the guard's env rule (`ARCHWRIGHT_*` and `GIT_CONFIG_GLOBAL` only) would block it, so the settings key is the better lever.
- **f1 `--safe-mode` kills the Works' hooks.** Hooks started 0/0 and the marker file was never written. It would blind the state machine. It also hides the delivered project skill. Not usable.
- **f2 `--restricted`** removes Bash and the code-running tools (22 tools), and ignores project settings and skills. Not usable for agents that must run `gh`, `git` and tests.
- **f3 `--disable-slash-commands`** removes all skills including the delivered ones. Not usable.
- **f4 `--bare`** fails login, as the `--help` text says, and skips hooks.

### What stays after isolation (b / g3)

19 skills: `guild-probe` plus Claude's bundled skills (`design`, `design-sync`, `dataviz`, `update-config`, `verify`, `debug`, `code-review`, `simplify`, `batch`, `fewer-permission-prompts`, `doctor`, `claude-approvals`, `loop`, `schedule`, `claude-api`, `run`, `run-skill-generator`, `plugin-authoring`). `CLAUDE_CODE_DISABLE_BUNDLED_SKILLS=1` (documented) would remove most of these; it is an env var, so the same allowlist problem as g2. The `disableBundledSkills` settings key (named on the same env-var entry) is not measured here (UNVERIFIED). The built-in `cc-plugin-*` plugins stay.

## Claude: managed policy

Not tested. This machine has no managed file or MDM policy, and I did not read the developer's server-managed cache. A denial I could trigger safely does not exist here. So every claim below is documentation, not measurement.

| Option | Does managed policy still apply? | Source |
|---|---|---|
| b. `--setting-sources` | Documented yes. Managed settings are their own tier above `--settings`, "Nothing in your own settings files or `--settings` overrides a managed key". `--setting-sources` takes "(user, project, local)" per `--help`; managed is not a value. The settings page also says a developer's left-out files do not count for managed exceptions "with an option such as `--setting-sources`", which confirms the flag only leaves out the three file scopes. | settings page, `claude --help` |
| c. `--strict-mcp-config` | Settings policy yes. But with a `managed-mcp.json` present the docs say the flag makes the CLI exit ("You cannot dynamically configure MCP servers when an enterprise MCP config is present"; `--strict-mcp-config` also exits). Not tested. Quoted from `docs/research/claude-code-headless-and-managed-settings.md`, which read the managed-mcp page; I did not re-read that page. | prior research |
| d. `CLAUDE_CONFIG_DIR` | **Weakened, and fails first.** Server-managed settings are fetched when the session authenticates with a qualifying org credential; an isolated dir has no login (measured), so a Session would not start at all. File and MDM policy (system directories) are not under the config dir, so they would still apply if the login worked. | measurement, prior research |
| e. b + c | As b, plus the managed-mcp exit risk of c. | as above |
| g3 (recommended) | As b. `disableClaudeAiConnectors` is a restrictive key, honoured from any scope. Managed `allowManagedHooksOnly` would still block the non-managed hooks the Works supplies, in every option: that is the ADR 0027 "hooks unavailable" degrade and is not new. | settings page, hooks page |
| `--safe-mode` / `--restricted` | Help text says managed (policy) settings still apply (safe mode: "Admin-managed (policy) settings and the hooks of managed plugins still apply"; restricted: "managed settings and `--settings` still apply"). | `--help` |

UNVERIFIED and worth one test on a managed machine (the Mac): that under `--setting-sources project,local` a command the managed allow list denies is still denied, and that `/status` still lists the managed source. Suggested check in the tickets below.

## Codex: measurement

What I could observe: skills (via `debug prompt-input`), auth (via `exec`). I could not observe MCP servers, plugins, hooks or managed requirements safely (no MCP servers are configured here, and hook firing is not visible in `exec --json` output).

| Cell | How | Skills seen (guild-probe) | Auth | Notes |
|---|---|---|---|---|
| a. today | `codex debug prompt-input "Reply with OK"` in `$S/proj` | 34 entries (yes): roots `~/.agents/skills` (r0, includes 3 claude.ai-synced skills and the developer's), `~/.codex/skills/.system` (r1, bundled), project `.agents/skills` (r2, the delivered one) | n/a | `codex exec --ephemeral --json` returned `OK`, exit 0 |
| `--ignore-user-config` | `codex exec --ignore-user-config --ephemeral --json "Reply with OK"` | not shown by `exec` | ok, `OK`, exit 0 | The flag text: "Do not load `$CODEX_HOME/config.toml`; auth still uses `CODEX_HOME`". Whether it also skips user `hooks.json`, MCP servers and plugins: UNVERIFIED (not observable here). Only exists on `codex exec`. |
| `CODEX_HOME=<empty dir>` | `debug prompt-input` | 34 (yes): identical list | not tested (would need login) | User skills come from `~/.agents/skills`, not from `CODEX_HOME`. So an isolated `CODEX_HOME` does **not** remove them. |
| `HOME=<empty dir>` | `debug prompt-input` | 5 (yes): bundled only plus the project skill | not tested | Removes `~/.agents/skills`, and would also move the default `CODEX_HOME`, so login is lost unless `CODEX_HOME` points back at the developer's home. That last combination I did not run: Codex writes logs and databases into `CODEX_HOME`, and I may not write there. UNVERIFIED. |

Documented layer rules (Codex docs): skills are read from `.agents/skills` from the working directory to the repo root, `$HOME/.agents/skills`, `/etc/codex/skills` and bundled; a skill is disabled by a `[[skills.config]]` entry in `~/.codex/config.toml` with `enabled = false`. Hooks are discovered in `/etc/codex/hooks.json`, `$CODEX_HOME/hooks.json`, trusted project `.codex/hooks.json`, inline `[hooks]`, plugins and managed `requirements.toml`; "Matching hooks from multiple files all run"; "Non-managed hooks must be reviewed and trusted before they run"; "Managed hooks are trusted by policy". Project `.codex/` layers load only when the project is trusted, and "User and system layers remain separate and still load". The skills page does not rank the locations. The config page does not mention `--ignore-user-config` or `/etc/codex` and gives no MCP keys.

What the Works already does (`internal/session/codex.go`): every Codex Session runs with a Works-owned `CODEX_HOME` containing the Works' `hooks.json` and links to the developer's `auth.json`, `config.toml`, `rules` and `AGENTS.md`. So the developer's own Codex `hooks.json` is already not loaded, and `config.toml` (which carries their `mcp_servers`, plugin and skill settings, and the folder trust entries) is. That is the existing isolation seam for Codex; whether Codex follows the links and records trust through them is already marked UNVERIFIED in `docs/spec/tmux-runtime.md`.

Managed policy under Codex: not tested. This machine has no `/etc/codex`. Prior research says managed requirements are enforced under `--ignore-user-config` (`docs/research/effective-permissions-claude-codex.md`, a documentation reading, not a test). Managed hooks and rules in `requirements.toml` are not in `$CODEX_HOME`. UNVERIFIED here.

## Options evaluated for Sessions

| Option | Claude | Codex | Cost to the developer | Verdict |
|---|---|---|---|---|
| 1. Leave as is | 63 skills, the developer's plugins, hooks and connectors load | the developer's skills, config, MCP load (hooks not) | none | Acceptable only if the Guild's skills can be steered (#160) and non-Forge personal MCP is accepted. |
| 2. `--setting-sources project,local` | skills 63 to 19, user plugin and hooks gone, connector stays | no equivalent for interactive Codex | loses user permission rules, default mode, env, model; MCP connector remains | Good start, incomplete alone. |
| 3. Isolated config dir | **login fails** (measured), loses server-managed policy | `CODEX_HOME` already isolated by the Works; `~/.agents/skills` still loads | cannot start a Session | Rejected for Claude. For Codex it is what exists already. |
| 4. Mix | b + connector off (g3) | stop linking `config.toml` as-is, or link a filtered copy | as option 2 | **Recommended** (see below). |

## Recommendation: option 4, built from b and `disableClaudeAiConnectors`

Claude:
1. Add `--setting-sources project,local` to the Works' own Claude launch line (the adapter adds it, like `--settings` today).
2. Add `"disableClaudeAiConnectors": true` to the hooks settings file the Works writes (`writeSettings`).
3. Leave `--strict-mcp-config`, `--mcp-config`, `CLAUDE_CONFIG_DIR`, `--bare`, `--safe-mode` and `--restricted` alone.

Codex: no change in the first step. Record the finding that user skills and (via the linked `config.toml`) user MCP servers still load, and decide later whether to link a filtered `config.toml`.

What it costs the developer in a Claude agent Session (all from the documented scope of `--setting-sources`; permission and default-mode effects not measured, UNVERIFIED):
- Their user-level permission rules stop applying. The one that matters most: the allow rule for `archwright tool` that ADR 0014 asks them to add "in their own settings". Without it every tool call prompts in the pane, so the rule must live in project or local scope of the Site, which the Works must not write (ADR 0027). Options: the developer commits it to the Site's `.claude/settings.json`, or the Works documents it, or the Works supplies it via `--settings` (blocked by the never-list's "never add an allow rule", ADR 0027). This is the real blocker and needs a maintainer decision before the guard change.
- `archwright doctor`'s `prompt:git-push` and `prompt:gh` checks read user settings; under isolation they describe a different effective policy. They must read the same scopes the Session will load.
- Their `defaultMode`, `model`, `env` and their own hooks, plugins, agents and skills are not available to agents. That is the intent.
- Their claude.ai connectors (Atlassian, Slack, and so on) are not available to agents. Intent; agents still reach the Forge through `gh`.
- A user-scope MCP server in `~/.claude.json` (not present on this machine) is not touched by `--setting-sources`, since that file is not a settings file. UNVERIFIED. If it matters, `--strict-mcp-config` is the lever, with the managed-mcp.json exit risk above.

What stays the same: login, managed policy (documented), the Works' hooks and state tracking (measured), delivered project skills (measured), the folder trust dialog, and the pane as the only permission gate.

### Exact amendments and guard change

All of this is for the maintainer to apply; I changed none of it.

- **ADR 0003** is already superseded by ADR 0026. No further edit. Optionally add one line to ADR 0026: "A Claude Session loads only project and local settings; the Works supplies the rest (hooks and a connector opt-out) through its own settings file."
- **ADR 0005**: amend "every run sees a pinned, isolated set" to say it is isolated from the Guild's point of view only: the Session also sees Claude's bundled skills and, on Codex, the developer's `~/.agents/skills`. Today the sentence overstates.
- **ADR 0011**: no edit (superseded by 0027). Add a note to the migration trail only if desired.
- **ADR 0014**: amend. Replace "The developer allows `archwright tool` to run without a prompt once, in their own settings" with where the rule must now live under `--setting-sources project,local`. Also update "Hooks supplied through `--settings` run under the observed policy": they are the only source of hooks besides managed and project ones.
- **ADR 0022**: no edit (superseded by 0028).
- **ADR 0027**: amend the never-list wording. "never pass `--settings` or `--mcp-config` that overrides managed keys" stays true; add that the Works passes `--setting-sources project,local` itself and that `--setting-sources` can only select among user, project and local scopes, so it cannot remove managed policy. Also add that the Works' own settings file may carry restrictive keys (`disableClaudeAiConnectors`) and never allow rules.
- **Guard (`internal/runner/guard.go`)**: no change is needed for the Works' own flag, because the Claude adapter writes its own argv (as it does for `--settings`) and `forbiddenFlags` only inspects `Spawn.ExtraArgs`. Keep `--setting-sources`, `--settings`, `--mcp-config` and `--strict-mcp-config` forbidden there so a caller cannot widen or redirect them. If the maintainer prefers to also let a caller pass one, it is the same single flag, but I do not recommend it. Separately, `-p`, `--bare` and `--ignore-user-config` are stale entries for Sessions (not wrong; `-p` and `--bare` are still harmful to pass, and `--ignore-user-config` does not exist on interactive Codex).
- **`docs/spec/tmux-runtime.md`**: update the launch line and the "Hooks are registered through a settings file for Claude (`--settings`, merged with managed hooks)" sentence to show `--setting-sources project,local`. Under that flag the settings file is the only source of non-managed hooks; project hooks in the Site still load.

## Suggested tickets

1. **Verify on a managed machine (blocker).** On the Mac with the company policy: run the same cells a, b, g3 against a command the managed allow list denies and one it allows, interactively, then check `/status` lists the managed source, the Works' hooks fire, the trust dialog and project-skill load work, a user-scope MCP server in `~/.claude.json` is or is not gone, and what the `archwright tool` prompt does with the user rule removed. Output: the UNVERIFIED cells here turned into results. Sessions needed: one interactive Session per cell.
2. **Decide where the `archwright tool` allow rule lives** under `--setting-sources project,local`, then amend ADR 0014 and ADR 0027, and fix `archwright doctor` to read the scopes a Session loads.
3. **Claude adapter: add the flag and the key.** `claudePlan` adds `--setting-sources project,local`; `writeSettings` adds `disableClaudeAiConnectors: true`. Tests: argv contains the flag; the settings file carries the key and no `permissions.allow`. Update the spec and ADR 0026 line.
4. **Doc amendments**: ADR 0005, 0014, 0027 as listed, and a note in `docs/spec/tmux-runtime.md`.
5. **Codex follow-up (separate, lower priority).** Decide whether the Works links `config.toml` as is or a filtered copy (dropping `mcp_servers`, plugins, `skills.config`) while keeping the trust entries; check that `~/.agents/skills` cannot be isolated without `HOME` changes (measured: it cannot with `CODEX_HOME` alone); and check whether the Works can add `[[skills.config]] enabled = false` entries to the Works-owned config. Note that `-c` is on the never-list. Verify managed `requirements.toml` hooks and rules apply in the Works-owned `CODEX_HOME`.
6. **Optional: Guild-skill steering (#160).** Independent of all of the above; isolation reduces the menu but does not replace it.

## Commands used

Set once (all paths under scratch, never the repo):

```
S=/tmp/claude-1000/-home-phillip-Development-archwright/7e0d3a43-2740-48cf-b6ef-c2e271c03616/scratchpad
mkdir -p $S/proj/.claude/skills/guild-probe $S/proj/.agents/skills/guild-probe $S/emptycfg $S/emptycodex $S/emptyhome
cd $S/proj && git init -q .
# $S/proj/.claude/skills/guild-probe/SKILL.md: frontmatter name: guild-probe, description: probe skill; body "Measurement only."
cp $S/proj/.claude/skills/guild-probe/SKILL.md $S/proj/.agents/skills/guild-probe/SKILL.md
# $S/hook.sh appends "<epoch> $1" to $S/hook-marker.txt; $S/works-hooks.json registers it for SessionStart and UserPromptSubmit
# $S/works-hooks-noconn.json is that file plus "disableClaudeAiConnectors": true
```

Claude cells (substitute the extra flags or env for each cell; `$S/sum.py` prints skills, MCP servers and status, plugins, agents, tool count, hook_started counts, permission mode, result from the stream; it prints no credential field):

```
cd $S/proj
claude -p "Reply with OK" --output-format stream-json --verbose --include-hook-events --settings $S/works-hooks.json                                   > $S/a.raw   # a
claude -p ... --settings $S/works-hooks.json --setting-sources project,local                                                                          > $S/b.raw   # b
claude -p ... --settings $S/works-hooks.json --strict-mcp-config                                                                                      > $S/c.raw   # c
CLAUDE_CONFIG_DIR=$S/emptycfg claude -p ... --settings $S/works-hooks.json                                                                            > $S/d.raw   # d (Not logged in)
claude -p ... --settings $S/works-hooks.json --setting-sources project,local --strict-mcp-config                                                      > $S/e.raw   # e
claude -p ... --settings $S/works-hooks-noconn.json                                                                                                   > $S/g1.raw
ENABLE_CLAUDEAI_MCP_SERVERS=false claude -p ... --settings $S/works-hooks.json                                                                        > $S/g2.raw
claude -p ... --settings $S/works-hooks-noconn.json --setting-sources project,local                                                                   > $S/g3.raw
claude -p ... --settings $S/works-hooks.json --safe-mode | --restricted | --disable-slash-commands | --bare                                           # f1..f4
python3 -I $S/sum.py $S/<cell>.raw ; cat $S/hook-marker.txt      # delete the marker before each run
```

Codex:

```
cd $S/proj
codex --help | grep -c ignore-user-config          # 0: not an interactive flag
codex exec --help | grep -A1 ignore                # --ignore-user-config, --ignore-rules
codex debug prompt-input "Reply with OK" > $S/cx_a.json
CODEX_HOME=$S/emptycodex codex debug prompt-input "Reply with OK" > $S/cx_d.json
HOME=$S/emptyhome        codex debug prompt-input "Reply with OK" > $S/cx_h.json
codex exec --ephemeral --json "Reply with OK" < /dev/null
codex exec --ignore-user-config --ephemeral --json "Reply with OK" < /dev/null
codex mcp list
```

Docs read: `code.claude.com/docs/en/{settings,mcp,hooks,env-vars,managed-settings,skills}`; Codex `config-advanced`, skills and hooks pages (now at learn.chatgpt.com/docs). The managed-settings, MCP and hooks pages were longer than one fetch returned, so parts were read by search (`allowManagedHooksOnly`, `allowManagedMcpServersOnly`, login keys); the full `managed-mcp.json` page was not re-read and its claim comes from `docs/research/claude-code-headless-and-managed-settings.md`.

## Not verified

- Interactive-Session behaviour of every Claude cell (the proxy is headless).
- Managed policy under any option, Claude or Codex.
- Permission rules, `defaultMode`, `env` and model settings being dropped by `--setting-sources` (documented scope, not observed).
- A user-scope MCP server in `~/.claude.json` under b (none on this machine); project-scope `.mcp.json` approval in interactive.
- macOS Keychain behaviour for a different `CLAUDE_CONFIG_DIR`.
- Codex: MCP servers, plugins and user-hook loading under `--ignore-user-config`; `HOME` override with `CODEX_HOME` pointed back at the real home (not run, it would write under `~/.codex`); Codex following the links in the Works' `CODEX_HOME`.
- `disableBundledSkills` and the Works' env allowlist for `CLAUDE_CODE_DISABLE_BUNDLED_SKILLS`.
