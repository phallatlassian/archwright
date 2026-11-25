# Forge actions stay Works-brokered; an agent Forge session was prototyped and not adopted

ADR 0022 has the Works run every Forge action itself, from its own process with the developer's stored `gh` credentials. We tested the alternative: a **Forge session**, one long-lived interactive `claude` in `auto` mode kept in tmux, to which the Works hands a fully specified action. We keep ADR 0022 unchanged.

The session works. A prototype on a managed Mac (issues #97, #100) showed a PreToolUse hook injected through `--settings` can allow only the Works-built argv, run it outside the sandbox, and deny an injected "run something else"; the result comes back through PostToolUse (success) or PostToolUseFailure (exit code in `error`); `/clear` via `send-keys` gives a fresh context; it costs about 5-8 seconds warm per action. But it only matches what the broker already does, and the broker is built and its core proven: the Works runs `gh` outside the sandbox without trouble (issue #79). Against that, the session adds a tmux dependency, an LLM turn and its cost per action, a possible auto-mode classifier refusal, reliance on hook payloads that are not a stable contract, a Claude-only first version, and a deliberate exception to ADR 0011's rule that Archwright never sets `dangerouslyDisableSandbox`. No driver beyond "`gh` works" was found for it: the broker's one real weakness, an environment where the Works cannot run `gh`, has not been observed.

Not adopted, kept as a primary source: the prototype on branch `prototype/forge-session-tmux` and the notes in the closed tickets (#96, #97, #98, #99, #100). Also considered and dropped: making Forge a subagent of the Archwright (unreachable between turns and by Wardens and Wrights, shares the Archwright's sandbox, no Codex equivalent) and moving the Archwright into tmux (changes ADR 0001's launch flow for no Forge benefit).

## Revisit if

- The Works cannot run `gh` on a real install (no Keychain access for the daemon, no `gh` on its PATH): the Forge seam of ADR 0022 would then get a session-backed adapter, built from the prototype, with the ADR 0011 exception written at that point.
- The scope widens to `pnpm`, `go test`, installs: a session that runs arbitrary build commands is a different decision.

Superseded by ADR 0028, with ADR 0022 (a note only).
