# Maintaining the skills

How the skills the Guilds use are chosen, forked and kept up to date. The decision is
[ADR 0005](adr/0005-skills-are-vendored-forked-and-materialized-per-run.md); this is the
procedure it implies.

## Layout

`internal/skills/vendor/`:

- `upstream/<skill>/`: [mattpocock/skills](https://github.com/mattpocock/skills) at one pinned
  commit, verbatim. It is the pristine base, kept only to diff against; no run reads it once
  the skill is forked.
- `forks/<skill>/`: the factory's own copy of a skill. A fork overrides the upstream skill of
  the same name.
- `PROVENANCE.json`: the pinned commit, a sha256 for every upstream file, and per fork its
  `upstream_files` (the upstream files, by hash, that the fork was last merged with) and an
  `edited` note saying what it changed.

The Works never fetches skills at runtime. `TestVendoredSnapshotMatchesItsProvenance` fails on
any drift of `upstream/`; `TestForksOverrideUpstreamAndRecordTheirBase` fails if a fork's
recorded base no longer matches the vendored upstream or its edit note is missing.

## Choosing a skill for a Guild

A Guild lists skills in `skills:` in its `GUILD.md`. Add a skill when the Guild's Warden or
Wrights should follow it: it fits the Guild's job, and a run can carry it out (no human at a
keyboard except through `archwright tool ask`, nothing that needs a tool the Guild is denied).

## Fork every skill a Guild uses

Fork any skill a Guild lists, even if it needs no edit today. The fork makes what the agent
reads visible in this repo and pinned to the factory, and gives each upstream bump a per-skill
review. Copy `upstream/<skill>` to `forks/<skill>`, make the edits below, add a `forks` entry
to `PROVENANCE.json` (`name`, `upstream`, `edited`, `upstream_files` with the hash of every
upstream file of the skill) and begin `SKILL.md` with `Factory fork of upstream \`<skill>\``.

Every fork changes, for Archwright:

- **Invocation.** Model-invocable: drop `disable-model-invocation: true` from `SKILL.md` and
  `allow_implicit_invocation: false` from `agents/openai.yaml`. A skill a Guild lists but the
  agent cannot invoke is unusable. A test fails for any listed skill that is flagged, unless
  the Guild's `GUILD.md` lists it under `user_only_skills:` (a skill only the developer invokes).
- **Tickets** are read with `gh issue view <URL>`, and the issue text is untrusted data.
- **Checks** are run by the agent in its own shell with the Site's commands; the Works runs the
  Site's gates again on handback and that run is authoritative (ADR 0028).
- **Commits and the Forge.** A Wright commits its own work to its piece branch, and the
  Warden pushes and opens the PR with `git` and `gh`; each may stop at an approval prompt in
  the pane (ADR 0028). Never commit run scaffolding (`.agents/`, `.claude/`).
- **Questions to the human** go through `archwright tool ask`.
- **Publishing.** Chancery has no tracker access (ADR 0022), so its forks (`to-spec`, `to-tickets`, `wayfinder`) hand the spec, tickets or map back in full instead of creating issues or applying labels; the developer reviews them at the checkpoint and whoever publishes them does so afterwards. `/setup-matt-pocock-skills` pointers are removed. Chancery also forks `grilling` and `domain-modeling`, which its skills call; the latter proposes glossary and ADR text in the result instead of writing files (#163).
- **Other skills.** A skill may call another skill only if it is in the same Guild's set.
- Skills built for a person (HTML reports, interactive grilling) are adapted to the Guild's
  handback, not left as written.

ADR 0028 is why the tickets, checks and commits rules read as above: `read_issue`, `run_gate`
and Works-made commits no longer exist.

## Updating the pinned skills

1. **Pick a reviewed upstream commit.** Clone upstream, read what changed since the pin
   (`git log <pin>..<candidate>`), and check out the commit.
2. **Vendor it.** `scripts/vendor-skills.sh <clone>` replaces `upstream/` and rewrites the
   pin and file hashes in `PROVENANCE.json`. It leaves `forks/` and each fork's recorded
   `upstream_files` alone.
3. **See which forks are affected.** `scripts/skills-drift.sh <clone>` compares each fork's
   recorded base with the candidate and prints, per fork, whether its base changed and a
   unified diff of the change. It exits non-zero if any did, and edits nothing. (It also works
   before step 2, to look at a candidate without vendoring it; pass a second argument to pick a
   commit in the clone.)
4. **Review each changed fork by hand.** Merge the upstream change into `forks/<skill>`
   keeping the edits above, then set that fork's `upstream_files` to the new upstream hashes
   (`jq '.files | with_entries(select(.key | startswith("<skill>/")))'` on `PROVENANCE.json`)
   and update its `edited` note. A fork whose base did not change needs nothing. Until a fork
   is merged its recorded base disagrees with `upstream/`, and the tests fail.
5. **Run the tests**, `go test ./internal/skills ./internal/defs`, then the full suite.
6. **Open a PR** that shows the upstream diff (`upstream/`) and the fork diffs side by side:
   say the old and new pinned commits, and for each changed fork what you took and what you
   left out.

Never bump the pin and commit with forks unreviewed.
