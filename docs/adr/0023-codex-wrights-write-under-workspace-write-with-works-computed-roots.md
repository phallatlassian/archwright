# Codex Wrights write under workspace-write with Works-computed writable roots

A headless Codex Wright cannot edit files or commit: `codex exec` is read-only by default, and under `workspace-write` the git directory is still protected, and for a Works worktree it sits outside the workdir, in the Works' bare clone. ADR 0022 said the Works would broker a `commit` tool "rather than widening runner permissions". Scratch-repo tests (`docs/research/forge-broker-sandbox-spike.md`, issue #106) show a narrow widening works instead, and avoids brokering file edits too.

For a Codex step whose role writes (a Wright step), the adapter passes `-s workspace-write` and one `-c` key, `sandbox_workspace_write.writable_roots`, with a list the Works computes and validates: the run's own worktree gitdir plus the shared clone's `objects`, `refs` and `logs` for a Works worktree, or the repo's `.git` for a plain repo. The Works picks the mode per step; a Guild or agent never supplies it. Warden, review and research steps keep the runner's default. `danger-full-access`, `--add-dir`, every other `-c` key and any approval-policy change stay refused, and the guard accepts the one `-c` key only when the Works built it.

This is a bounded exception to ADR 0011's rule that a mode never be more permissive than the runner's default, and it supersedes the "broker a `commit` tool" fallback in ADR 0022. There is no brokered `commit` tool for Codex (issue #105 is obsolete). If managed policy disallows `workspace-write`, the bind fails with a clear error and `archwright doctor` prints the remedy; there is no fallback to a looser or brokered path.

Accepted consequences: the shared `objects`, `refs` and `logs` are writable by every Codex Wright on a Site, so one Wright could alter another's branch (Claude Wrights already share the clone's git directory the same way); isolating per run would need a clone per run and break ADR 0019. Git prints a non-fatal `packed-refs.lock` error on commit (exit 0); it is tolerated and noted in the Codex notes rather than fixed with another writable root.

Superseded by ADR 0027: Codex starts at its default mode, so the workspace-write exception is obsolete.
