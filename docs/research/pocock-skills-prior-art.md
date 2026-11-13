# Prior art: Warden-shaped and router skills in mattpocock/skills

Ticket AW-4. Facts only; Archwright vocabulary used where it clarifies, not as claims about the source.

**Source.** github.com/mattpocock/skills at commit `4ae2317ddb22b046e26455e4f9c9b3aaaf62212d`, read 2026-11-12 via `gh api`. All paths below are `skills/engineering/<name>/SKILL.md` unless noted. Line numbers refer to those files. (Note: the repo has a top-level `skills/` dir plus `docs/engineering/<name>.md` pages; the latter were not read.) `implement` (referenced by ask-matt) was not read, out of the ticket's list.

## Cross-cutting facts

- **Tracker/config dependency.** Every skill below except `research` says some form of: "The issue tracker ... should have been provided to you. If not, tell the user to run `/setup-matt-pocock-skills`." (implement-spec L9, code-review L13, to-spec L9, to-tickets L11, triage L43, wayfinder L25). Config is not a runtime file read by code: setup writes `docs/agents/issue-tracker.md`, `docs/agents/triage-labels.md`, `docs/agents/domain.md` and an `## Agent skills` block in `CLAUDE.md`/`AGENTS.md` (setup SKILL L49, L69-86), which is how the config gets "provided". This repo already has that block (AGENTS.md) so it is satisfied.
- **Setup itself is interactive** ("prompt-driven skill, not a deterministic script. Explore, present what you found, confirm with the user, then write", setup L15; "Let them edit before writing", L72). Local tracker = `.scratch/<feature>/` (setup L46).
- **`disable-model-invocation: true`** in frontmatter of implement-spec, to-spec, to-tickets, triage, wayfinder, ask-matt, setup. Only `code-review` and `research` omit it. I.e. these are designed as human-invoked slash commands; this is an invocation flag, not a statement that they cannot run headless (uncertainty: whether a harness can still call them via the Skill tool; wayfinder and implement-spec themselves instruct the Skill tool to call other skills, e.g. `tdd`, `research`, `code-review`).
- **Subagent conventions** repeated: "Start every subagent with a fresh context window" (implement-spec L15, code-review L11, research L6, wayfinder L115); communicate via "context pointers" not duplicated content (implement-spec L17).

## Per-skill

### implement-spec (445 words)

- **Decomposes:** a spec + its tickets, read as a "task graph with blocking relationships" with a "frontier" of ready tickets (L13, L23).
- **Dispatches:** optional exploration subagent writing notes outside the repo (L25); one fresh implementer subagent per ticket, each in its own worktree/branch, in background for concurrency (L19, L29); a merger subagent per completed implementer (L34); re-reads frontier and launches more (L36); one fresh implementer for code-review fixes (L38). Each implementer calls Skill `tdd` and merges integration tip before reporting (L29-32).
- **Inspects:** only at the end: Skill `code-review` on the integration branch (L38). No per-ticket inspection by the orchestrator; merger step is the only gate.
- **Inputs:** spec and tickets (from to-spec/to-tickets), tracker config. **Outputs:** one integration branch; draft PR marked ready if tracker closes via PRs, else tickets resolved "the way the issue tracker closes work" (L40); worktrees cleaned (L42).
- **Interaction:** none specified after start; no user prompts in the text beyond "tell the user to run setup" (L9). Appears runnable non-interactively. Does not mention what to do on test failure or implementer failure (gap). Does not cap parallelism.

### code-review (1094 words)

- **Decomposes:** one diff into two axes, Standards and Spec (L6-9).
- **Dispatches:** two parallel foreground sub-agents with fresh context (L60). Standards agent gets diff command, standards file list and the 12-smell Fowler baseline pasted in full, "under 400 words"; Spec agent gets diff and spec text (L62-72). Spec agent skipped if no spec (L74).
- **Inspects:** git diff `<fixed-point>...HEAD`. Aggregates verbatim under `## Standards` / `## Spec`; explicitly "Do not merge or rerank" (L78), ends with per-axis counts and worst issue per axis (L80). Reports only, does not fix.
- **Inputs:** fixed point (commit/branch/tag); asks if missing (L19). Spec found via commit-message issue refs, user path, `docs/`/`specs/`/`.scratch/` file, else ask (L27-32). Standards: any repo file documenting standards, `CODING_STANDARDS.md`/`CONTRIBUTING.md` mandatory (L36). Preflight: ref resolves and diff non-empty (L23).
- **Interaction:** asks only when fixed point or spec are unresolvable; with those supplied (e.g. by implement-spec) it is non-interactive. Only skill here without `disable-model-invocation`.

### triage (990 words; + AGENT-BRIEF.md 1232 words, OUT-OF-SCOPE.md not read)

- **Inspects/decomposes:** issues (and optionally external PRs) into 2 category roles (`bug`, `enhancement`) and 5 state roles (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`), exactly one of each (L26-41). State machine L45.
- **Dispatches:** no subagents. Calls Skill `grilling` and `domain-modeling` "if needed" (L76).
- **Process:** gather context incl. redundancy check and `.out-of-scope/*.md` prior-rejection check (L70); recommend and "Wait for direction" (L72); verify claim by reproducing bug / running PR (L74); grill; apply outcome (L78-86).
- **Outputs:** labels, comments each prefixed with `> *This was generated by AI during triage.*` (L13-17), agent-brief comment for `ready-for-agent` (AGENT-BRIEF.md: "the authoritative specification that an AFK agent will work from ... the agent brief is the contract"; no file paths/line numbers, behavioural, acceptance criteria, explicit scope boundaries), `.out-of-scope/` entries, closures.
- **Interaction:** maintainer-driven: "The maintainer invokes `/triage` and describes what they want in natural language" (L49); recommendation then wait (L72); grilling is HITL. Not designed to run autonomously; the only low-interaction path is "Quick state override" (L88-90), which still "Confirm[s] what you're about to do".

### ask-matt (1958 words + PHASE-BOUNDARIES.md 699 words)

- **Router.** Reads no tracker, dispatches nothing. Text-only guide: a "main flow" (grill-with-docs -> optional prototype/handoff detour -> to-spec -> to-tickets -> implement or implement-spec -> tdd + code-review -> pr -> retro, L15-34), "on-ramps" (triage for external issues, diagnosing-bugs, wayfinder, L42-54), codebase-health, vocabulary layers, standalone skills, precondition setup (L95).
- **Routing criteria it states** (usable as facts): multi-session build? yes -> to-spec/to-tickets (L24-28); well-scoped feature vs foggy huge effort -> wayfinder ("never a well-scoped feature", L52); triage only for issues "you didn't create" (L48); wayfinder "hands off, doesn't build", merges back at `/to-spec` (L54).
- **Instruction to the router:** "Before stating what a skill does or recommending a step be skipped, read that skill's SKILL.md: the summaries here are for orientation only" (L11), so routing is by static description, not computed.
- **Context hygiene rules:** keep grilling->to-spec->to-tickets in one window; `/clear` between tickets; smart zone ~150k tokens (L38-40). PHASE-BOUNDARIES.md holds a 5-option decision tree (continue, clear, handoff, subagent, compact).
- **Interaction:** conversational with a human. It is a human-facing lookup; it assumes the user picks the path. Not a dispatcher: no handoff protocol to the skills, no state.

### wayfinder (2080 words)

- **Decomposes:** a vague, too-big effort into a **map** issue (label `wayfinder:map`; sections Destination / Notes / Decisions so far / Not yet specified / Out of scope, L31-53) and child **decision tickets**, each with `## Question` and exactly one label `research | prototype | grilling | task` (L59-65). "Plan, don't do": produces decisions, not deliverables (L11-13). Map is "an index, not a store" (L23).
- **Dispatches:** in chart mode, step 5 fires one fresh-context subagent per `research` ticket, each calling Skill `research`, writing findings to a throwaway `research/<name>` branch, pushed, no PR (L115). Everything else is one ticket per session: "never resolve more than one ticket per session, with the exception of research tickets" (L105).
- **Inspects:** frontier = open, unblocked, unclaimed children (L69). Blocking via tracker-native links; claim = assign ticket to self first (L67, L123).
- **Ticket types and who drives:** Research AFK; Prototype HITL; Grilling HITL ("the agent never stands in for the human's side", L75); Task HITL or AFK (L77-80).
- **Inputs:** a loose idea from a human; tracker config incl. a "Wayfinding operations" section in the tracker doc (L25). The local tracker doc defines these: map `.scratch/<effort>/map.md`, tickets `.scratch/<effort>/issues/NN-<slug>.md`, `Type:` / `Status: claimed|resolved` / `Blocked by:` lines, `## Answer` heading (issue-tracker-local.md L21-30). If no tracker, defaults to local markdown (L25).
- **Outputs:** map + tickets; per-resolution: resolution comment, close, append pointer to Decisions-so-far (L125); new tickets/fog graduation (L126).
- **Interaction:** chart mode steps 1-2 are explicit grilling with the user (L111-112) and it may stop and ask whether a map is needed at all (L112). Work mode on research/task-AFK tickets is non-interactive; grilling/prototype tickets need a human. So: partially AFK by design, the ticket label is the AFK/HITL switch.
- **Note vs this project:** AW-4 itself is a `wayfinder:research` ticket, i.e. this repo already runs wayfinder on the local/otman tracker.

### to-spec (493 words)

- **Decomposes:** nothing; synthesises current conversation + codebase into a spec (L7). Template: Problem Statement, Solution, User Stories (long numbered), Implementation Decisions (no file paths), Testing Decisions, Out of Scope, Further Notes (L21-75).
- **Dispatches/inspects:** no subagents. Explores repo; uses glossary vocabulary and respects ADRs (L13); proposes test seams (prefer one, highest) and "Check with the user that these seams match" (L15-17).
- **Inputs:** conversation context (explicitly "Do NOT interview the user", L7), tracker + triage vocabulary. **Outputs:** spec issue published to tracker, labelled `ready-for-agent`, "no need for additional triage" (L19).
- **Interaction:** one confirmation point (seams). Otherwise no interview but entirely dependent on prior conversation context; cannot synthesise without that input. Non-interactive only if the context is supplied (e.g. a wayfinder map's linked decisions, per ask-matt L54).

### to-tickets (917 words)

- **Decomposes:** a plan/spec/conversation into tracer-bullet vertical slices, each sized "to fit in a single fresh context window", with blocking edges (L27-38). Wide refactors handled as expand-migrate-contract tickets (L40). Prefactoring ticket first (L23, L34).
- **Dispatches/inspects:** none. Step 4 "Quiz the user" on granularity, edges, merge/split, "Iterate until the user approves" (L42-56).
- **Inputs:** conversation or a spec path / issue ref fetched with comments (L17); tracker config. **Outputs:** local: `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, one file per ticket, blockers first, template with `Status: ready-for-agent` + acceptance criteria (L62, L69-82); real tracker: one issue per ticket in dependency order with native blocking links, sub-issues of the source, label `ready-for-agent` (L63). "Do NOT close or modify any parent issue" (L67).
- **Interaction:** approval loop is mandatory by the text; step 4 is the only gate. Skipping it is possible only by deviating from the skill.

### research (138 words)

- **Dispatches:** one background agent, fresh context (L6). Its job: investigate primary sources ("official docs, source code, specs, first-party APIs, not a secondary write-up"), cite each claim, write a single Markdown file where the repo "already keeps such notes; match the existing convention, and if there is none, put it somewhere sensible and say where" (L8-12).
- **Inspects:** nothing; no verification step, no format template, no uncertainty requirement in the text (the "uncertainty stated" convention is Archwright's, not the skill's).
- **Inputs:** a question. No tracker, no setup dependency. **Outputs:** one markdown file. **Interaction:** none; fully AFK. It is the only skill here that is a pure Wright-shaped unit (question in, file out). Wayfinder resolves `research` tickets by calling it (wayfinder L77).

## Summary table

| Skill | Dispatches subagents | Verifies/inspects | Needs tracker config | Human gate in text | Non-interactive? |
|---|---|---|---|---|---|
| implement-spec | yes: explorer, implementer per ticket, merger, fixer | only final code-review | yes | none | yes (gaps: failure handling, parallel cap) |
| code-review | yes: 2 parallel | is the inspection | yes (for spec lookup) | asks only if fixed point/spec missing | yes given inputs |
| triage | no | verifies claims by reproduction | yes (+labels) | recommend-then-wait; grilling | no |
| ask-matt | no | no | no (points at setup) | conversational | n/a, human lookup |
| wayfinder | research subagents only | frontier query | yes (+wayfinding ops) | chart steps 1-2; HITL tickets | partly: research/AFK-task only |
| to-spec | no | no | yes | seams check | only with prior context |
| to-tickets | no | no | yes | mandatory approval loop | not as written |
| research | the one background agent | no | no | none | yes |

## Observations relevant to Archwright (facts, minimal inference)

1. Only `implement-spec` both dispatches workers and merges their results, and it has no per-worker inspection step before merging beyond a merger agent; its only review is one `code-review` at the end. `code-review` splits and aggregates but is read-only. Neither "accepts a task then splits, dispatches, inspects before it leaves" as one contained unit: Warden behaviour is distributed across implement-spec + code-review (inference).
2. Handoffs between skills are via the tracker and files (specs, tickets, map, research notes, `.scratch/`), not via conversation. This is stated as design in implement-spec L17 (context pointers) and wayfinder L23 (map as index).
3. The HITL/AFK split is explicit in wayfinder (ticket labels) and ask-matt/triage (`ready-for-agent` vs `ready-for-human`). triage's agent brief is the written "contract" for AFK work.
4. ask-matt is static documentation, not executable routing; it holds the decision criteria as prose.

## Uncertainty

- Did not read `implement`, `grilling`, `domain-modeling`, `tdd` sources from the repo (called by the above); installed local copies under `~/.claude/skills` not compared to the repo version.
- Did not read `docs/engineering/*.md` pages, `agents/openai.yaml` files, `OUT-OF-SCOPE.md`, or the GitHub/GitLab tracker docs beyond the local-tracker wayfinding block.
- "Non-interactive?" judgements are from reading skill text, not by running the skills.
