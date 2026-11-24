# tmux runtime: mechanics

Mechanics for ADRs 0026 to 0029. The decisions are in the ADRs; this document holds the details an implementer needs. Evidence: `docs/research/tmux-interactive-sessions.md` (branch `research/tmux-interactive-sessions`) and the resolution comments of issues #3 to #7 in `talvor/archwright-tmux`. Tested with tmux 3.8, claude 2.1.285 and codex-cli 0.171.0 on a managed Mac. Anything not verified there is marked UNVERIFIED.

## Launch lines

One Session per run, in a pane of the `factory` tmux session on the dedicated socket (`tmux -L archwright`, owned by the Works). The brief is the first prompt, passed as the positional argument; for a long brief the Works writes it to a file and passes a short pointer to it.

```
tmux -L archwright -f <works.tmux.conf> new-window -d -t factory: -n <job-slug> -c <worktree> -e K=V ... '<runner command>'
claude --permission-mode default --settings <hooks.json> [--session-id <uuid>] [--model <m>] "<brief or pointer>"
CODEX_HOME=<works codex home> codex -C <worktree> --no-alt-screen [-m <m>] -- "<brief or pointer>"   # hooks from a stable file, see Trust
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
- The pause between paste and Enter is the Works' to set (default 200 ms), and the state is checked again after it: a Session that has gone to a prompt in between gets no Enter. `paste-buffer -p` brackets the paste where the CLI asked for it, so a line break in the text is not an Enter. A Nudge is pasted as it is only when it is one short line that is not read as a command (no leading `/`, `!`, `#`, `@` or `&`); anything else is written to a file in the run's directory and a one-line pointer is pasted (ADR 0029). A delivery with no UserPromptSubmit within the timeout (default 15 s) is reported unconfirmed and never retried: the text may be in the composer.
- Rework (ADR 0029): a piece sent back by its Warden is Nudged into its Wright's open Session, in the worktree and on the branch it already has. The Session still holds the environment of its first run, which has ended, so the Nudge's file carries the amended brief and the `export` lines of the new run (`ARCHWRIGHT_RUN`, `ARCHWRIGHT_SPOOL`, `ARCHWRIGHT_RUN_TOKEN`). A Session that is working or at a prompt is waited for (default 2 minutes, nothing typed meanwhile), then the rework fails: no second Session is started beside a live one. If the Session is gone, a fresh Session runs the amended brief in a new worktree from the previous round's branch (or, when the Session ended just as the rework arrived, in the worktree the previous round used).
- Never send Enter or paste at a prompt. At a permission or approval prompt, pasted text is dropped or ignored and Enter approves option 1 (tested on both CLIs). An interrupt (Esc or Ctrl-C) at a prompt approving it is UNVERIFIED.
- Mid-turn behaviour differs, so a Nudge never relies on it: Claude queues the pasted text and delivers it as the next turn; Codex steers it into the running turn (same turn id).
- Busy Codex Session: `codex queue --thread <session_id> --message <text>`, with `session_id` from the hook payload. Mid-turn it is queued, not steered, and delivered as a new turn after `Stop` (new turn id, UserPromptSubmit fires).
- `capture-pane` is not an input check: Claude's prompt-suggestion ghost text appears in it, so composer text is not necessarily pending input.
- A Stop with no handback gets one Nudge, then a Needs-you item (ADR 0029).

## Standing Wardens, the Archwright and closing a Job's Sessions

Mechanics for ADR 0029.

- A Warden's Session is kept per Job and Guild, a Wright's per Job and piece, so two Jobs never share one and a Review Warden (the Review Guild's) never lands in a builder's. The first step for a Guild launches the Warden; each later step, and each later review of the same checkout, is a Nudge into it: a file with the new brief and the `export` lines of the new run (as for rework), and a pointer pasted. Runs for one Session are taken one at a time, so steps of a Guild that are started together are carried out in turn in its Warden; a Session that is working or at a prompt is waited for like a Wright's, then the run fails.
- A review is a read-only Session (the Review Guild is read-only, so plan mode) of that Guild's own Warden, in the review's detached checkout. Its second pass in the same checkout is a Nudge into the same Session; a review in a different checkout launches a fresh Session there (so its directory and delivered skills are that review's) and cancels the superseded one.
- Closing: the Works looks once a second at the Jobs that hold Sessions. A Job has left active work when its PR is recorded or it is closed (accepted or discarded) or purged. After a retention (default 30 s) each of its Sessions that is idle or already ended is cancelled and its pane removed, so the Job's window goes with the last pane; a Session that is working, at a prompt or starting is left and asked again. Not yet specified: a Job that fails without closing (parked, or cancelled steps) stays in active work, so its Sessions stay.
- The Archwright: `archwright` asks the Works to spawn it (`POST /archwright`), and attaches to `factory:archwright` with `TMUX` cleared. The Works launches it in the neutral factory home in the `archwright` window, replacing the empty pane the factory session starts with (and the dead pane of a previous Archwright). Its first prompt is its instructions; there is no start context and no hook that delivers the inbox: the Archwright reads it with `read_inbox`. A Works that does not host Sessions answers 501 and the CLI runs the Archwright in its own terminal, as before. The Works cancels the Archwright when it stops; adopting it after a restart is the recovery slice.
- The inbox wake is a Nudge with a fixed one-line text carrying no item content. It is sent when an item is unread that the Archwright has not been Nudged about, it is idle and has finished its first turn; a busy Archwright is asked again each time it goes idle. One Nudge per new item: an Archwright that ends its turn without reading is not Nudged again until something new arrives. A Nudge whose delivery is not confirmed is not repeated and is recorded as a `nudge.failed` event.

## Asks and open Sessions

Mechanics for the Ask amendment in ADR 0029.

- A Wright's blocking Ask is recorded, routed up and held in the Needs-you inbox as before (first answer wins, never expires). The dispatch parks as `blocked`, but its Session is not closed: the Wright ends its turn, the Session goes idle and waits, and nothing is typed at it while the Ask is open.
- The answer, from the Warden, the Archwright or the dashboard, makes a fresh dispatch of the piece (an answer is not a rework round). When the Wright's Session is still open the dispatch is carried out in it: a Nudge in the worktree and on the branch it already has, as for rework. Its file carries only the Q&A record (the Session holds the brief) and the `export` lines of the new run. A Session that is working or at a prompt is waited for like a rework's (default 2 minutes, nothing typed meanwhile), then the run fails.
- If the Session is gone, a fresh Session runs the original brief plus the Q&A record, as it did before Sessions stayed open.
- Not yet specified: a Warden's own blocking Ask. Its step still fails when its run ends; the answer is read with `show_ask` when it is next summoned.

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
- Codex has no flag for a hooks file elsewhere; it reads `$CODEX_HOME/hooks.json`, `<repo>/.codex/hooks.json`, `config.toml` and plugins. The Works therefore gives every Codex Session one Works-owned `CODEX_HOME` (`<state>/tmux/codex-home`) holding a `hooks.json` that is identical for every run and rewritten only when its content changes (run identity travels in the pane's environment, not the file), with `auth.json`, `config.toml`, `rules` and `AGENTS.md` linked from the developer's own Codex home; the developer's `hooks.json` is never touched. Trust is accepted once against that file. UNVERIFIED against a real Codex: that it follows those links and records trust through them.
- Codex has no plan or dontAsk mode and the never-list forbids its approval and sandbox flags, so a Codex Session runs at its default and a narrower mode fails the launch.
- Codex hook trust is keyed by source. `-c` hooks are keyed by position and overwrite each other's trusted hashes, so Codex hooks must come from a stable file (`hooks.json` or `config.toml` at a fixed path). `--dangerously-bypass-hook-trust` is on the never-list. Managed Codex hooks are already trusted.

## Review before push

Review before push is Plan ordering only: the `open-pr` step is blocked by Review. Nothing enforces it on the push itself. Agents of any role may run `gh` and `git push` as the developer, and the pane approval is the only gate on a push (ADR 0028).
