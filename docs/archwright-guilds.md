# Archwright — The Guilds

The departments of the software factory: what each one is for, what arrives at its door,
what leaves it, and a story about it doing its job.

A **Guild** is a standing body responsible for one *kind* of work. It is defined by its
craft, not by the project it happens to be working on, and it persists across tasks: a
Guild is where the tools, conventions and quality standards for that craft live. Each has
a **Warden** that accepts whole tasks from the **Archwright**, breaks them into work,
dispatches **Wrights**, and inspects what comes back before any of it leaves the Guild.

For the layers themselves — Archwright, Guild, Warden, Wright — see
[archwright-layers.md](./archwright-layers.md). For how the
[mattpocock/skills](https://github.com/mattpocock/skills) set maps onto these guilds, see
[skills-to-guilds.md](./skills-to-guilds.md).

Context for spec work, not the spec.

## The roster

| Guild | Craft | Handback |
|---|---|---|
| **Chancery** | Intent into work orders | A spec, or a set of tickets with declared dependencies |
| **Survey** | Establishing what is true | A claim with evidence and stated uncertainty |
| **Masonry** | Changing code | A diff that compiles, passes its tests, and matches the codebase |
| **Hallmark** | Inspecting work | A verdict against standards and against the spec |
| **Chapter** | Naming and recording | A settled term, a glossary entry, an ADR |
| **Millwright** | Maintaining the factory | A change to the factory's own instructions and checks |

Roughly the order work moves through them, though nothing forces that path: a small fix
goes straight to Masonry, and a question goes straight to Survey.

---

## Chancery — intent into work orders

**Charter.** Turn what a developer wants into instruments the rest of the factory can act
on: a spec, or a set of tickets each declaring what blocks it.

**Intake.** An ambition, usually too large and too vague to build from — a sentence, a
complaint, a strategy.
**Handback.** Authorised work orders. Where the ambition is too big to hold in one
session, a map of decision tickets instead, to be resolved one at a time.
**Draws on.** `to-spec`, `to-tickets`, `wayfinder`, `grill-with-docs`, `to-questionnaire`.

A chancery was the writing office of a monarch or state: the secretariat that drafted,
sealed and archived official instruments. The royal Chancery issued the *writs* that
started legal actions — nothing proceeded until Chancery drew up the instrument
authorising it. In this factory a ticket is a writ. No Wright touches code that no writ
authorised.

> **A story.** A developer tells the Archwright: *"We need to go multi-region."*
>
> There is nothing here to build. The Archwright recognises it as Chancery's work and
> hands it over whole.
>
> The Chancery Warden does not start writing tickets, because it does not yet know what
> the developer means — multi-region read replicas and multi-region writes are different
> projects with a year between them. It establishes, first, that the ambition is too large
> to hold in one plan, and that several of its unknowns block each other. So it does not
> produce a spec. It produces a map: eleven decision tickets, each one a question whose
> answer constrains the others — *do writes need to be region-local?*, *is the session
> store replicated or re-established?*, *what consistency do we promise an operator mid-
> failover?* — with the blocking edges drawn between them.
>
> It hands that map back, along with the one ticket at the frontier: the question whose
> answer unblocks the most others. Nothing has been specified and nothing has been built.
> But the shape of the work is now visible, and the next job is a question, not a guess.
>
> Three weeks later, with eight of those decisions resolved, the same Warden takes the
> accumulated answers and does its other job: it writes the spec, and splits it into
> tracer-bullet tickets, each small enough for one Wright and each declaring the tickets
> it waits on.

**Does not:** decide whether the ambition is worth pursuing, or build anything.

---

## Survey — establishing what is true

**Charter.** Answer questions about the world, the codebase, and the documentation, with
evidence attached.

**Intake.** A question whose answer changes what gets built.
**Handback.** A claim, its evidence, and an honest statement of what remains uncertain.
**Draws on.** `research`, `improve-codebase-architecture`, and codebase investigation —
which has no skill yet and will need writing.

A survey establishes the lie of the land before anyone builds on it; the Domesday survey
was the original. Measurements come back with their error stated, and a surveyor does not
build.

Survey's tools are read-only. That is the whole of its discipline: it cannot change
anything, so it cannot be tempted to fix what it finds. Its definition of a finished job
is not *a working change* but *a claim someone else can act on without re-deriving it*.

> **A story.** The Archwright needs to know whether the payment provider's v3 API can carry
> the idempotency guarantees the current integration relies on, before anyone commits to a
> migration. It hands Survey the question.
>
> The Warden splits it three ways and dispatches Wrights: one reads the provider's v3
> documentation and changelog, one traces every place the current code depends on
> idempotent retries, one checks what the provider's own client library actually sends.
>
> The answers do not agree. The documentation says idempotency keys are honoured for 24
> hours; the client library sets a header the documentation does not mention; and the
> codebase relies on a retry window of seven days that nothing in v3 appears to support.
>
> A weaker Warden would pick the documentation and call it answered. This one hands back
> the disagreement: *v3 honours idempotency keys, but only for 24 hours as documented; our
> current integration assumes seven days; the client library's extra header is
> undocumented and may or may not extend it. The seven-day assumption is load-bearing in
> the refund path. This cannot be resolved from public sources — someone must ask the
> provider.*
>
> That is a complete handback. It stopped a migration that would have broken refunds, and
> it named the one thing a human has to do.

**Does not:** change code, recommend a course of action as though it were a finding, or
present a guess with the same confidence as a citation.

---

## Masonry — changing code

**Charter.** Build the thing, to the standard, with proof that it works.

**Intake.** A ticket or spec specific enough to implement, with its constraints stated.
**Handback.** A diff, passing tests, and anything the Guild noticed that exceeds its remit.
**Draws on.** `implement`, `implement-spec`, `tdd`, `diagnosing-bugs`, `prototype`, with
`codebase-design` as a standing standard every brief inherits.

Masons built to a lodge's standards, course by course, each stone set on those already
laid. Here the courses are tickets and the standards are the codebase's.

Masonry holds the conventions of the codebase — how modules are shaped, where seams
go, what gets tested through the interface. A task handed to Masonry comes back having
met those conventions whether or not the Archwright thought to ask.

> **A story.** Chancery's multi-region work has produced seven tickets with their blocking
> edges drawn. Four are unblocked. The Archwright hands the whole set to Masonry.
>
> The Warden works it as a task graph rather than a list. It reads the frontier — the
> tickets whose dependencies are all closed — and dispatches a Wright to each of the four
> in parallel: the region-aware config reader, the health-check endpoint, the connection
> pool change, and the migration that adds the region column.
>
> Three come back clean. The fourth, the connection pool, comes back with passing tests
> and a change the Warden rejects: the Wright hard-coded a two-region assumption into a
> loop, because its brief said "support two regions" and it had no way to know that ticket
> six adds a third. The Warden reissues it with the constraint it should have carried in
> the first place — *region count must be read from config, not assumed* — and notes the
> brief's failure as its own.
>
> With the migration closed, ticket five is now at the frontier. The Warden dispatches it
> without being asked, and keeps going until nothing is unblocked. Then it hands back one
> branch, one test run, and one line: *the region column has no backfill for existing rows;
> someone needs to decide what region they belong to.*

**Does not:** reinterpret what the ticket asked for, decide product questions it
encounters, or hand back work whose checks do not pass.

---

## Hallmark — inspecting work

**Charter.** Read finished work cold and say whether it is good, and whether it is what was
asked for.

**Intake.** A diff and the writ it came from.
**Handback.** A verdict on two axes, with each finding tied to a line and a reason.
**Draws on.** `code-review` (Standards and Spec as parallel sub-agents), `pr`, `triage`.

Goldsmiths' Hall tested metal against a legal standard and struck its mark on what
passed — the *hallmark*, a verdict stamped on the work itself. Work that failed got no
mark. Here the mark is the verdict: Standards and Spec, each finding tied to its reason.

The two axes are separate on purpose and are usually failed separately. **Standards** asks
whether the code follows the codebase's documented conventions. **Spec** asks whether it
does what the originating ticket actually asked. Work can pass either while failing the
other, and the second failure is the one that automated checks never catch.

> **A story.** A diff arrives from Masonry: an endpoint gains a cache, tests pass,
> types check, the style is impeccable. The Standards axis finds two small things and
> nothing more.
>
> The Spec axis reads the ticket instead of the code. The ticket said the endpoint was slow
> *under concurrent load for distinct users*. The cache is keyed on the request path. For
> distinct users, every request is a miss — the cache adds a lookup to the hot path and
> helps nobody. The benchmark in the PR shows a 40% improvement because it replays one
> user's traffic.
>
> Nothing here is broken. Every check passes. The work is well made and answers a question
> nobody asked.
>
> Hallmark hands back: *Standards pass with two minor notes. Spec fail — cache key does not
> match the stated access pattern; the benchmark does not reproduce the reported
> conditions. Reproduce with distinct users before re-reviewing.* The Archwright now has a
> real decision to put to the developer: was the ticket wrong, or was the fix?

**Does not:** fix what it finds, re-run the deterministic checks that already gated the
handback, or approve work whose spec axis it could not evaluate because no writ was
attached.

---

## Chapter — naming and recording

**Charter.** Decide what the project calls things, and write it down where the next agent
will read it.

**Intake.** A term that two parts of the system disagree about, or a decision worth
outliving the conversation that produced it.
**Handback.** A settled term in the glossary, an ADR, or a sharpened steering document.
**Draws on.** `domain-modeling`, `writing-for-agents`, `handoff`.

A chapter house is where a guild keeps its records. Survey finds out what is true;
Chapter decides what the project *calls* it, so that the next brief, the next ticket and
the next Wright all mean the same thing by the same word. `writing-for-agents` makes this
guild load-bearing beyond its own craft: every Wright brief in the factory is a document
written for an agent, and the brief is the highest-leverage artifact in the system.

> **A story.** The Millwright Guild reports a pattern rather than a bug: in four separate
> jobs, a Wright has mishandled something called a *tenant*. The diffs are unrelated. The
> briefs were all clear. The Wrights all did something reasonable.
>
> Chapter takes it and finds the cause in the vocabulary. Billing's *tenant* is a
> contracting entity with one invoice. Provisioning's *tenant* is an isolated database
> schema. A large customer is one billing tenant and nine provisioning tenants. Every brief
> that said "tenant" was clear, and half of them were clear about the wrong noun.
>
> The Warden does not rename anything in code yet — that would be a Masonry task, and
> a large one. It settles the language: *account* for the contracting entity, *tenant*
> reserved for the isolation boundary, with the glossary recording both and explicitly
> noting the trap. It writes the ADR for why the split exists, so the next person to
> propose unifying them finds the reasoning instead of repeating the argument. And it files
> one Chancery ticket: rename the billing usage in code, mechanically, in its own change.
>
> The next brief that needs the word is unambiguous, and so is every brief after it.

**Does not:** rename things in code, or record a decision the factory has not actually
made.

---

## Millwright — maintaining the factory

**Charter.** Keep the machinery that makes the product in good repair, and sharpen it from
what goes wrong.

**Intake.** A finished job worth learning from, or the factory's own failures.
**Handback.** A proposed change to the factory's own instructions, checks, navigation or
tooling — always for human review.
**Draws on.** `retro`, `setup-matt-pocock-skills`, `wizard`.

Millwrights install, align and maintain the machinery that makes the product. They do not
make the product. This guild's work changes how every other guild behaves, which is
exactly why its output goes to a human every time, and why its own failures must never
chain into further automated attempts.

> **A story.** The Millwright Warden reads a merged job end to end: the writ, the diff, the
> reviews, the run logs. It is looking for the same mistake twice.
>
> It finds it. In this job, as in two before it, a Wright spent most of its work
> rediscovering which of four packages owns the HTTP client — information that exists
> nowhere in the steering files and has to be re-derived by reading imports every time.
> Separately, the Spec axis of Hallmark failed once for a reason an automated check could
> have caught cold: a new endpoint shipped without the rate-limit annotation every other
> endpoint carries.
>
> It proposes two changes, and they are deliberately different in kind. The first is a
> navigation note in the steering file naming the owning package — cheap, and it stops
> three Wrights a week from re-deriving it. The second is a lint rule that fails any route
> handler lacking the annotation, which turns a judgment Hallmark had to exercise into a gate
> nothing can pass by accident.
>
> It files both as one change for human review, with the evidence from all three jobs
> attached. It does not apply either one itself. The factory does not get to quietly
> rewrite its own instructions.

**Does not:** change the product, apply its own proposals without human review, or retry
its own failures automatically.

---

## Launch roster

Not all six at once.

- **Masonry, Survey, Hallmark** — the minimum viable factory. Build, find out, inspect.
- **Chancery** — necessary rather than optional. Without it, every ambition lands on the
  Archwright, which overloads the one component that must stay able to reason about a whole
  job. It also has the strongest existing skill coverage of the three proposals.
- **Chapter** — a judgment call. Fold into Survey if the roster is getting wide; its
  stories only start to bite once enough briefs exist for terminology to drift.
- **Millwright** — defer until the factory is big enough to break itself. The role is real,
  but the available skills barely furnish it, so most of it is new work.

### Deliberately not guilds

- **Assay / QA.** Types, lint and tests are deterministic. They are a gate every handback
  must pass, not a craft exercising judgment.
- **Herald / comms.** Writing a PR body is one skill. It lives in Hallmark.
- **Release / delivery.** Premature until something actually deploys.

## Open questions

- **Who owns the frontier?** Chancery draws the dependency edges; Masonry's Warden
  walks them. If Masonry discovers the graph is wrong mid-job, does it re-plan, or hand
  back to Chancery through the Archwright?
- **Can a Guild commission another Guild?** Chapter's story has it filing a Chancery
  ticket, and Millwright's has it reporting a pattern to Chapter. Either those edges are
  real and guilds talk sideways, or everything routes through the Archwright and these
  stories need rewriting. The layers doc currently says vertical only.
- **What Hallmark inspects.** Only Masonry's diffs, or also Chancery's tickets, Survey's
  findings and Chapter's terms? Everything in the factory is work that could be inspected.
- **Survey without a skill.** Codebase investigation is this guild's commonest job and
  has nothing written for it. Until it does, Survey is one skill deep.
- **Where throttling lives.** If a Warden can dispatch its whole frontier in parallel, what
  caps it — the Guild, the Archwright, or the runner budget?
