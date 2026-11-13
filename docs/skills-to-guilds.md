# Skills to Guilds

How the skills in [mattpocock/skills](https://github.com/mattpocock/skills) map onto the
Archwright factory's guilds, which guilds are missing, and what the mapping exposes about
the architecture.

Companion to [archwright-layers.md](./archwright-layers.md), which defines the layers
themselves. Context for spec work, not the spec.

Read at commit depth 1 on 2026-11-12. **Scope: the `engineering/` (20) and `productivity/`
(7) folders only — 27 skills.** The repo's `misc/`, `in-progress/` and `deprecated/`
folders are out of scope and deliberately not mapped here.

---

## The three chosen guilds

### Masonry Guild — changes code

| Skill | Layer it belongs to | Note |
|---|---|---|
| `implement` | Warden intake | Build the work described by a spec or set of tickets. |
| `implement-spec` | **Warden** | Works tickets as a task graph, running implementer subagents across the ready frontier, closing with `code-review`. This is a Warden already written down — read it before designing ours. |
| `tdd` | Wright | Red-green-refactor on one vertical slice. The natural default brief for a change Wright. |
| `diagnosing-bugs` | Wright | For the "something is broken" intake: red feedback loop, minimise, hypothesise, instrument, fix, regression-test. |
| `prototype` | Wright | Throwaway, answers a design question. Output is a finding, not a diff. |
| `codebase-design` | Guild standard | Vocabulary for deep modules. Not a task — something the Guild holds and every Wright brief inherits. |

### Survey Guild — establishes what is true

| Skill | Layer it belongs to | Note |
|---|---|---|
| `research` | Wright | Question against high-trust primary sources, captured as cited Markdown. Already designed to run as a background agent. |
| `domain-modeling` | Wright | Or move to Chapter, below. |
| `improve-codebase-architecture` | Wright | Scans for deepening opportunities and reports. Written for a human (HTML report, then a grilling), so needs adapting before a Warden can consume it. |

**This guild is thin.** One genuinely core skill. The diagnosis step in a typical job —
trace the code paths, read the logs, establish the mechanism — has no skill behind it.
Expect to write one.

### Hallmark Guild — inspects

| Skill | Layer it belongs to | Note |
|---|---|---|
| `code-review` | **Warden** | Two axes, Standards and Spec, as parallel sub-agents. Already Warden-shaped: it splits, dispatches, and reports one result. |
| `pr` | Wright | PR body shape: smallest visual that makes the change clear, before/after evidence, merge-danger call (one-way or two-way door, blast radius). |
| `triage` | Warden | State machine over incoming issues and external PRs; writes agent-ready briefs. Those briefs are Wright briefs — see the brief contract question. |
| `retro` | — | Reviews the factory, not the code. Belongs to Millwright, below. |

---

## Proposed additional guilds

### Chancery — intent into work orders

`to-spec`, `to-tickets`, `wayfinder`, `grill-with-docs`, `to-questionnaire`

The largest hole in the three-guild roster. Turning a conversation into a spec, and a spec
into dependency-linked tracer-bullet tickets, is real craft with three skills behind it.
`wayfinder` is the heaviest skill in the repo (~2000 words) and exists specifically for
work too big to hold in one session — which is the Archwright's hardest case. Leaving all
of this on the Archwright overloads the one component that most needs to stay able to
reason about the whole job.

**Chosen name: Chancery.** A chancery is the writing office of a monarch, state or
bishop — the secretariat that drafted, authenticated, sealed and archived official
instruments. The English royal Chancery issued the *writs* that started legal actions;
nothing proceeded until Chancery drew up the instrument authorising it. That is exactly
what `to-spec` and `to-tickets` do, and it makes a ticket a writ.

Alternatives considered, both from the building trades: **Drawing Office**, where an
architect's intent becomes dimensioned drawings, and **Loft**, the shipbuilder's mould
loft that scales the designer's lines to full size and cuts the templates the shipwrights
work to. Either keeps the craft register the rest of the vocabulary sits in; Chancery
trades that for a sharper fit on the authorisation idea. Note the cost: "chancery" also
names a court of equity and an embassy's offices, and connotes bureaucratic delay in
ordinary English.

### Millwright Guild — the factory maintains itself

`retro`, `setup-matt-pocock-skills`, `wizard`

Millwrights install, align and maintain the machinery that makes the product. The name
lands on exactly the right role and stops competing with Archwright for the top slot.

Its one standing duty from this skill set is `retro`: read a finished piece of work and
propose improvements to the factory's own instructions, navigation, checks and tooling.
Because this guild changes the factory itself, its work should always reach human review,
and its own failures must not chain into further automated attempts.

Thinnest of the three proposals in skill coverage — three skills, only one of them a
recurring task. The role is real, but most of what this guild would do still needs
writing. Defer it until the factory is big enough to break itself.

### Chapter — domain knowledge and agent-facing docs

`domain-modeling`, `writing-for-agents`, `handoff`

A chapter house is where a guild keeps its records. GLOSSARY.md, ADRs, and the steering
files agents read are a distinct craft from research: Survey finds out what is true, the
Chapter decides what the project *calls* things and writes it down so the next agent
inherits it.

`writing-for-agents` is load-bearing beyond its own guild — it is the skill that keeps
every Wright brief legible, and the brief is the highest-leverage artifact in the factory.

Most droppable of the three proposals; fold into Survey if the roster is getting wide.

### Deliberately not guilds

- **Assay / QA.** Types, lint and tests are deterministic. They are a gate every handback
  must pass, not a craft exercising judgment. Make them a precondition on a Warden's
  handback instead of a department.
- **Herald / comms.** `pr` is one skill. Leave it in Hallmark.
- **Release / delivery.** Premature until something actually deploys.

---

## What the mapping exposes

### A third of the skills require a human to talk to

`grilling`, `grill-me`, `grill-with-docs`, `to-questionnaire`, `wait-what`, `teach`,
`wizard`, `ask-matt` all work by interrogating a person.

Our architecture says only the Archwright talks to the developer. So none of these can be
Wright work. They are **Archwright capabilities**, which means the Archwright needs a
defined interrogation mode alongside its routing mode — it must be able to stop, grill the
developer until the intent is sharp, and only then plan. That is a bigger Archwright than
the layers doc currently describes.

`wizard` is the sharpest case: it exists precisely for steps only a human can perform
(provisioning, credentials, third-party dashboards). The factory needs a way to hand a
human a task and wait for them.

### Prior art for the Archwright itself

`ask-matt` is a router over the repo's skills — the Archwright's dispatch problem, solved
once already at smaller scale. Worth reading before designing routing.

### Prerequisite

`setup-matt-pocock-skills` configures a repo's issue tracker, triage label vocabulary and
domain doc layout. The other engineering skills assume it has run. Whatever the factory
operates on needs it first.

---

## Open questions this raises

Additions to the list in [archwright-layers.md](./archwright-layers.md).

- **Is the Archwright stateful?** Does it hold a plan and accumulated findings across a
  whole job, or is it a router over durable state kept somewhere else (a tracker, a repo)?
  This determines whether Wardens persist between tasks and whether a job can survive
  longer than one agent session. Decide early; it propagates everywhere.
- **Guild roster at launch.** Masonry, Survey, Hallmark are given. Chancery looks
  necessary rather than optional. Millwright can wait until the factory is big enough to
  break itself. Chapter is a judgment call.
- **Skill as Warden vs. skill as brief.** `implement-spec`, `code-review` and `triage` are
  already Warden-shaped — they decompose, dispatch and inspect. Do we wrap them as Wardens,
  or reimplement the Warden and use them only as Wright briefs? Wrapping is faster and
  inherits their quality; reimplementing gives one consistent Warden contract.
- **Where deterministic checks attach.** Types, lint and tests are a gate, not a craft.
  Confirm they sit on the Warden's handback rather than anywhere else, and decide what a
  Warden does when a check fails and its Wright cannot fix it.
- **Survey's missing skill.** Codebase investigation — trace, read, establish mechanism,
  cite — has no skill. Write one, or widen `research` beyond primary-source documents.
- **How fast to let it run.** Each automated trigger added is an accelerator. Which trigger
  comes first, what throttles it, and what has to earn trust before the next one is added?
