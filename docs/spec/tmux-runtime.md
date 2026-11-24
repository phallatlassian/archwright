# tmux runtime: mechanics

Mechanics for ADRs 0026 to 0029. The decisions are in the ADRs; this document holds the details an implementer needs. Evidence: `docs/research/tmux-interactive-sessions.md` (branch `research/tmux-interactive-sessions`) and the resolution comments of issues #3 to #7 in `talvor/archwright-tmux`. Tested with tmux 3.8, claude 2.1.285 and codex-cli 0.171.0 on a managed Mac. Anything not verified there is marked UNVERIFIED.

## Launch lines

One Session per run, in a pane of the `factory` tmux session on the dedicated socket (`tmux -L archwright`, owned by the Works). The brief is the first prompt, passed as the positional argument; for a long brief the Works writes it to a file and passes a short pointer to it.

```
tmux -L archwright -f <works.tmux.conf> new-window -d -t factory: -n <job-slug> -c <worktree> -e K=V ... '<runner command>'
claude --permission-mode default --settings <hooks.json> [--session-id <uuid>] [--model <m>] "<brief or pointer>"
codex  -C <worktree> [-m <m>] "<brief or pointer>"          # hooks from a stable file source, see Trust
```

Rules:
- Default permission mode and approval policy only. The never-list (ADR 0027) is checked before every spawn; the adapter refuses and fails the step visibly.
- Never `--bare` for Claude (it skips hooks). Codex may use `--no-alt-screen` so `capture-pane` is readable.
- Environment: an allowlisted snapshot of the developer's shell environment, captured on each `archwright` CLI call and passed per spawn with `-e`, plus `ARCHWRIGHT_RUN`, `ARCHWRIGHT_SPOOL` and `ARCHWRIGHT_RUN_TOKEN` (ADR 0018). The allowlist contents and staleness policy are not yet specified.
- Resume by id: `claude --resume <session_id>`, `codex resume <session_id>`. That resume keeps context on both CLIs is UNVERIFIED (an item on issue #7's Mac recheck list).
- Started means the first SessionStart hook. With no hooks: pane alive and a transcript file appears. No start within about 60 seconds, or the pane dies first, fails the step "failed to start" and keeps the dead pane.

## Hook-derived state machine

States: `starting`, `working`, `waiting-on-permission`, `idle`, `ended`. State comes from hooks plus pane liveness (`pane_dead`).

| Event | Effect |
|---|---|
| `SessionStart` | `starting` to `idle` (first prompt is already submitted, so `working` on the next `UserPromptSubmit`); record `session_id`, `transcript_path` |
| `UserPromptSubmit`, `PreToolUse` | `working` |
| `PermissionRequest` | add to the outstanding set; state `waiting-on-permission` |
| `PostToolUse` | clears the matching outstanding PermissionRequest; if the set is empty, back to `working` |
| `Stop` | `idle`, only when the outstanding set is empty |
| `SessionEnd`, `pane_dead` | `ended` (SessionEnd has a 1.5 s budget; confirm with `pane_dead`) |
| Claude `Notification` | corroborating only; never changes state alone |

- Codex fires `Stop` while an approval is pending (about 2.6 s after PermissionRequest, same turn id), then PostToolUse and another Stop after the approval. `Stop` alone is therefore never idle: the Session is waiting while any PermissionRequest is outstanding, cleared only by the matching PostToolUse.
- Events are deduplicated on event kind plus turn id. Codex `-c` hooks produced duplicate SessionStart and UserPromptSubmit deliveries about 2.5 s apart.
- Claude can deliver a queued prompt's UserPromptSubmit before the first turn's Stop; ordering is not guaranteed.
- Codex steering a Nudge into a running turn produces no UserPromptSubmit.
- A UserPromptSubmit the Works did not send is recorded as human-touched.
- If hooks are unavailable the state is reconstructed from the transcript, `pane_dead` and silence, and the dashboard marks it unreliable (ADR 0027).

## Nudge rules

A Nudge is the Works delivering text to a Session. It is permitted only when state is `idle` with no outstanding PermissionRequest.

- Delivery: `load-buffer` then `paste-buffer`, a short sleep, then `send-keys Enter`; verify through the UserPromptSubmit hook. Do not use `send-keys -l <text>` immediately followed by Enter: text can be left unsent in Codex's composer.
- Never send Enter or paste at a prompt. At a permission or approval prompt, pasted text is dropped or ignored and Enter approves option 1 (tested on both CLIs). An interrupt (Esc or Ctrl-C) at a prompt approving it is UNVERIFIED.
- Mid-turn behaviour differs, so a Nudge never relies on it: Claude queues the pasted text and delivers it as the next turn; Codex steers it into the running turn (same turn id).
- Busy Codex Session: `codex queue --thread <session_id> --message <text>`, with `session_id` from the hook payload. Mid-turn it is queued, not steered, and delivered as a new turn after `Stop` (new turn id, UserPromptSubmit fires).
- `capture-pane` is not an input check: Claude's prompt-suggestion ghost text appears in it, so composer text is not necessarily pending input.
- A Stop with no handback gets one Nudge, then a Needs-you item (ADR 0029).

## Hook payload fields

Hooks run outside the tool sandbox as the CLI's own process. Both CLIs send JSON on stdin.

| Field | Claude | Codex | Use |
|---|---|---|---|
| `session_id` | yes | yes | Session identity; resume; `codex queue --thread` |
| `transcript_path` | yes (may lag the current turn) | string or null | raw content path (ADR 0017); with null, find by session id |
| `cwd` | yes | yes | match to the run's worktree |
| `hook_event_name` | yes | yes | state machine input |
| `turn_id` | not relied on | on turn-scoped events | dedupe |
| `permission_mode` | yes | yes | Posture; check it is the default |
| tool name and input | on tool and permission events | same | Needs-you item (tool, exact command) |

Claude events used: SessionStart, UserPromptSubmit, PreToolUse, PermissionRequest, PostToolUse, Notification, Stop, SessionEnd. Codex events used: SessionStart, UserPromptSubmit, PreToolUse, PermissionRequest, PostToolUse, Stop, SessionEnd. Hooks never return a decision or modify anything.

Delivery to the Works: the hook script appends the payload to the run's spool file and POSTs it to a loopback TCP listener or a unix socket the Works owns; the spool is the fallback if the POST fails. macOS unix socket paths must be under 104 bytes, so the socket lives under a short path. Hooks are registered through a settings file for Claude (`--settings`, merged with managed hooks) and a stable file-based source for Codex.

## tmux options

- Server on its own socket (`-L archwright`); the developer's own server is never touched.
- Works-supplied config (`-f`) that first sources the developer's `~/.tmux.conf`, then sets what the factory requires.
- `remain-on-exit on`, with `pane-died` hooks, so exit status (`pane_dead_status`) and the final screen can be read before cleanup.
- One `factory` session; window `archwright` for the Archwright; one window per Job named by the Job slug; panes for Wardens and Wrights.
- Layout: Warden pane left (about 40%), Wrights tiled right (`main-vertical`), re-applied as panes come and go.
- Pane identity: the pane id is stored per run in SQLite (canonical); pane options `@run` and `@role` are set for convenience; pane borders are labelled `role - piece - state`, derived and rebuilt after a restart.
- Attention: Needs-you item, `archwright attach`, and status markers on the pane and window; no automatic popups.
- Not yet specified: pane cap and overflow in a Job window, and how the dashboard links to live Sessions.

## Trust probe

No hooks fire before a CLI's folder-trust dialog is answered, so a Session spawned into an untrusted directory sits at the dialog with no signal. The Works never writes trust configuration (ADR 0027).

- At Site registration the Works launches a probe Session in a throwaway worktree of the Site, with the same layout a Job uses, and raises a Needs-you item asking the developer to accept folder trust once. For Codex it also captures hook trust ("Trust all and continue") once. `archwright doctor` re-checks.
- Claude does not inherit trust from a parent folder into git repositories or worktrees; both CLIs inherit trust from a trusted repository to its worktrees, and Codex also covers subfolders (tested on Linux). Whether worktrees of the Works' bare clone (ADR 0019) inherit trust on the Mac is UNVERIFIED.
- Codex hook trust is keyed by source. `-c` hooks are keyed by position and overwrite each other's trusted hashes, so Codex hooks must come from a stable file (`hooks.json` or `config.toml` at a fixed path). `--dangerously-bypass-hook-trust` is on the never-list. Managed Codex hooks are already trusted.

## Review before push

Review before push is Plan ordering only: the `open-pr` step is blocked by Review. Nothing enforces it on the push itself. Agents of any role may run `gh` and `git push` as the developer, and the pane approval is the only gate on a push (ADR 0028).
