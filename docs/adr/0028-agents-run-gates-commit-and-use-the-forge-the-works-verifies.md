# Agents run gates, commit and use the Forge themselves; the Works verifies

A Session runs with the developer's own permissions and can prompt, so the workarounds that kept the Works in the middle are removed. A Wright runs the Site's checks in its own shell and commits its own work to its piece branch. `run_gate`, Works-made WIP commits and the `merge_base` tool are deleted. This supersedes ADR 0025 in part.

Kept from ADR 0025, restated here: the Works runs the Site's registered gates on a Snapshot of the Wright's worktree at handback, outside any agent, and that gate run stays authoritative; the Archwright trusts the Works' record, not an agent's claim of green. The Works also still merges an accepted piece branch into the Job's integration branch (fast-forward where possible); a conflict becomes a merge Wright task. A Warden never does a mechanical merge.

Forge access is direct. Agents of any role run `gh` and `git push` as the developer, and only the pane approval gates them. All brokered Forge tools are deleted, which supersedes ADR 0022 and, with it, ADR 0024 (a note only: its prototype and revisit conditions are moot). One recording tool remains: `archwright tool record_pr` (Warden role) takes the PR's url and number, is validated against the Site's remote, and stores it on the Job; one PR per Job. The Works reads PR and CI state itself for the dashboard and does not depend on an agent to report it. ADR 0008's original shape returns: the Warden pushes and opens the PR.

Review before push is Plan ordering only: the `open-pr` step is blocked by Review (ADR 0012, ADR 0021), and nothing enforces it on the push itself. We say so plainly. The hard risk to keep visible: with agents pushing themselves and hooks observe-only, the developer's approval in the pane is the only gate on a push. Which roles may use the Forge is not limited here, and whether `gh` and `git push` prompt under the developer's policy is checked by `archwright doctor` but not otherwise probed.

Supersedes ADR 0022 and ADR 0024, and ADR 0025 in part. Amends ADR 0005's forked `pr` skill (it reverts to plain `gh`), ADR 0008 and ADR 0012 (see their footers).
