# Forge broker spike: managed-sandbox facts (#79)

Run 2026-11-19 on the developer's managed machine (`claude 2.1.285`, `codex-cli 0.162.1`). Settles the open assumptions in ADR 0022. Real headless runs through the same flags the adapters use (`claude -p --permission-mode M`, `codex exec --json --ephemeral`, stdin closed), inherited sandbox, scratch bare clone with linked worktrees (gitdir outside the cwd), a stand-in spool responder.

## Results

| # | Question | Claude | Codex |
|---|---|---|---|
| 1 | `archwright tool` under `dontAsk` | Approved, ran, response read | n/a (no permission mode; see below) |
| 1 | `archwright tool` under `default` | Approved, ran, response read | n/a |
| 2 | `git add` + `git commit` in a worktree | **Works** | **Fails** (see below) |
| 3 | Works runs `gh` outside the sandbox | Issue read, push, PR create all work | same (Works-side) |

### 1. `archwright tool` approval

- **Claude:** approved with no denial under both `dontAsk` and `default`. The approval is **not** the user allow rule: re-running with `--setting-sources project,local` (user `Bash(archwright tool:*)` excluded) gave the same result. It is not the classifier either (`dontAsk` has none). Managed `allowManagedPermissionRulesOnly: true` ignores that user rule, and no managed rule mentions `archwright`. Likely cause: the sandbox auto-allows Bash that runs inside it (same reason `git commit`, `curl` and file writes inside the cwd were approved with no rule). UNVERIFIED as the exact mechanism; the observable fact is that it passes with no user rule in both modes. So `doctor`'s existing `tool:claude` check is meaningful and the `Bash(archwright tool:*)` rule is not needed on this machine.
- **Codex:** no permission mode. `archwright tool` ran because of a user exec-policy rule, `~/.codex/rules/default.rules`: `prefix_rule(pattern=["archwright", "tool"], decision="allow")`. That rule makes the command run **outside** the Seatbelt sandbox. It matches only a command that starts with `archwright tool`: the same call inside `sh script.sh` ran sandboxed and failed with `archwright: writing request: open /tmp/.../.tmp-*: operation not permitted`. Without the rule codex would be sandboxed read-only and unable to write the spool. A developer without that rule gets a failing transport; `doctor`'s `transport:codex` check passes here only because of the rule.

### 2. `git commit` in a Works worktree

Layout: bare clone, worktree created with `git worktree add`, `.git` file pointing at `<clone>.git/worktrees/<name>` (outside the cwd), as the Works lays them out.

- **Claude: works** under both modes. `git add` and `git commit` succeeded and `git log` showed the commit, while a write outside the cwd (`echo x > ../outside.txt`) was refused (`operation not permitted`), so the sandbox was active. Claude's sandbox evidently allows a linked worktree's gitdir, as its docs say, bare clone included. Note: the commit was unsigned (`%G?` = N) although global git config has `commit.gpgsign=true` (ssh signing); not investigated.
- **Codex: fails**, in both postures tried:
  - Default `codex exec` (what Archwright runs): the sandbox is effectively read-only. Even `echo x > spike.txt` in the cwd fails (`sh: spike.txt: Operation not permitted`), so a Codex Wright cannot edit files at all, before git is involved.
  - Diagnostic only, `-s workspace-write` (never passed by Archwright; the guard forbids it): cwd writes work, but git still fails because the gitdir is outside the writable roots:
    ```
    fatal: Unable to create '<clone>.git/worktrees/<name>/index.lock': Operation not permitted
    ```
    (`git add`, and `git commit` with or without `-c commit.gpgsign=false`, all fail the same way.) Reads (`git log`) work.
- Caveat on method: when asked to run 7 commands one per call, the codex model skipped several and reported plausible but invented errors. The evidence above comes from a single scripted command whose output is captured verbatim in the run's `command_execution` item. Treat free-form codex self-reports as unreliable.

### 3. `gh` outside the sandbox (the Works' situation)

Unsandboxed, using the developer's stored `gh` login (keyring, ssh protocol):

- Issue read: `gh issue view 79 --repo phallatlassian/archwright --json ...` returned the issue.
- Push: `git push origin <branch>` over ssh to `talvor/archwright-fixture` created the branch.
- PR create: `gh pr create --repo ... --head --base --draft` opened a PR.
- Throwaway PR #10 and the branch were then closed and deleted. (A first run of `gh` inside the sandbox failed TLS verification, as in #73.)

## Outcome for ADR 0022

- Claude Wrights can commit locally; no `commit` tool is needed for them.
- Codex Wrights cannot commit, and under default `codex exec` cannot edit files either. Per ADR 0022 the Works should broker a `commit` tool without widening runner permissions (ticket filed). The file-editing gap is wider than `commit`: a broker `commit` alone does not help a Codex Wright that cannot write the working tree. That needs its own decision (out of scope for #79; noted in the ticket).
- The Forge tools (issue read, push, PR open and comment) are viable: the Works holds working `gh` and git credentials.
- `doctor` should state that the claude tool approval does not depend on the user allow rule here, and that codex's tool call relies on an exec-policy rule that must start with `archwright tool`.
