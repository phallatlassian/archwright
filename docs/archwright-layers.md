# Archwright — Layers of the Software Factory

Working vocabulary for an agent-driven software factory. The names come from the
English building and guild trades: *-wright* means maker (shipwright, wheelwright),
*arch-* means chief. **Archwright** is the native-stock form of *architect*, which is
itself Greek for "chief builder."

This document defines four layers and tells a short story about what each one does.
It is context for spec work, not the spec.

## The layers at a glance

| Layer | Term | One-line role |
|---|---|---|
| Overseer | **Archwright** | The only agent the developer talks to. Holds the plan, routes work, owns the outcome. |
| Department | **Guild** | A standing body of expertise for one kind of work (Masonry, Survey, Docs, Hallmark). |
| Department lead | **Warden** | Accepts a task from the Archwright, breaks it into work, assigns it, inspects what comes back. |
| Worker | **Wright** | Does one scoped piece of work and returns the result. Knows nothing of the wider plan. |

Vertical only: the developer speaks to the Archwright, the Archwright speaks to Wardens,
a Warden speaks to its own Wrights. No layer reaches past its neighbour, and Wrights
never speak to each other.

---

## Archwright — the overseer

The Archwright is the single face of the factory. A developer describes what they want
in whatever form they want it — a sentence, a ticket, a half-formed complaint — and
everything after that is the Archwright's problem. It decides what the request actually
means, what sequence of work satisfies it, which Guilds are involved, in what order, and
what "done" looks like. It holds the plan and the accumulated state. When the work is
finished it reports back in one voice, as though it had done the whole thing itself.

Critically, the Archwright does not do the work and does not manage individual workers.
It never reaches into a Guild to assign a task to a particular Wright. That boundary is
what keeps it able to reason about a large job at all: it thinks in tasks and
deliverables, not in file edits.

> **A story.** A developer says: *"The rate limiter is dropping requests under burst and
> I don't know why. Fix it."*
>
> The Archwright does not start reading code. It recognises two different kinds of work
> hiding in one sentence — something is not understood, and something needs changing —
> and that the second depends on the first. It opens a plan with three steps: find out
> why, change the code, prove the change holds. Step one goes to the Survey Guild as a
> task: *establish the mechanism by which the rate limiter drops requests under burst
> load; cite the code paths and any evidence from logs or tests.*
>
> It then waits. When the Survey Warden returns a finding — the token bucket refills on
> a wall-clock tick, so a burst arriving just before a tick is starved — the Archwright
> records it, and only now writes step two with enough specificity to be actionable:
> *change bucket refill from tick-based to elapsed-time-based; preserve the configured
> rate; do not change the public config surface.* That goes to the Masonry Guild.
> Step three, a test that reproduces the burst and fails before the change, goes out with
> it as part of the same task, because the Archwright knows that validating work is the
> Warden's job and wants the proof built where the change is built.
>
> When Masonry returns, the Archwright has three artifacts: a diagnosis, a diff, and
> a test. It composes one answer for the developer — what was wrong, what changed, how we
> know — and surfaces the one thing it could not decide on their behalf: the fix alters
> burst behaviour at the boundary, and someone should confirm that is acceptable.

**Owns:** intent, decomposition, sequencing, cross-Guild state, the final report,
escalation to the human.
**Does not:** write code, read files for its own sake, assign work to individual Wrights,
re-do a Warden's inspection.

---

## Guild — the department

A Guild is a standing body responsible for one *kind* of work. Masonry changes code.
Survey establishes what is true. Others follow as the factory grows — Docs, Hallmark,
Release. A Guild is defined by its craft, not by the project it is working on, and it
persists across tasks: it is where the tools, conventions, and quality standards for that
craft live.

The guild framing carries real weight. A historical guild did not merely supply labour;
it set the standard for its trade, trained its members, and was answerable for the quality
of anything leaving its hall. A task handed to the Masonry Guild comes back having
met Masonry's standards, whether or not the Archwright thought to ask.

> **A story.** The Survey Guild and the Masonry Guild both receive work about the
> same rate limiter on the same afternoon, and they behave like different organisations,
> because they are. Survey's tools are read-only — search, trace, read, query logs — and
> its definition of a finished job is *a claim with evidence attached and its uncertainty
> stated*. Masonry's tools write to disk and run builds, and its definition of a
> finished job is *a change that compiles, passes its tests, and matches the conventions
> of the surrounding code*. Neither needs to be told these things per task. They are
> properties of the Guild.

**Owns:** craft standards, tool access for that craft, what "finished" means for its kind
of work.
**Does not:** hold the overall plan, or know what other Guilds are doing.

---

## Warden — the department lead

Each Guild has a Warden. It accepts a whole task from the Archwright, decides how that
task is actually carried out, splits it into independent pieces, dispatches those pieces
to Wrights, and — the part that matters most — inspects what comes back before any of it
is allowed to leave the Guild.

The name is chosen for precisely that last duty. In a real guild the wardens conducted the
*search*: they inspected members' work against the trade's standard and had the authority
to reject anything substandard. A Warden that merely forwards its Wrights' output upward
is not doing its job. It reads the work, checks it against the task it issued and the
Guild's standards, and sends it back for another pass if it falls short. The Archwright
should be able to trust a Warden's handback without re-inspecting it.

> **A story.** The Masonry Warden receives: *change bucket refill from tick-based to
> elapsed-time-based; preserve the configured rate; do not change the public config
> surface; include a test that reproduces the burst and fails before the change.*
>
> It first looks at the shape of the job. Three pieces, two of which do not depend on each
> other: write the failing test, change the refill logic, and — noticed by the Warden, not
> requested by the Archwright — check whether anything else in the codebase reads the
> bucket's tick field, because elapsed-time refill will make that field meaningless. It
> sends the first two to Wrights in parallel and the third to a Wright of its own
> initiative.
>
> The work comes back. The test reproduces the burst and fails on the old code: good. The
> refill change is correct in isolation but the Wright, with no sight of the wider task,
> renamed a config key to match the new semantics — which violates the "do not change the
> public config surface" constraint the Wright never saw. The Warden catches it and
> reissues that piece with the constraint spelled out. The third Wright reports two call
> sites reading the tick field, one of them in a metrics exporter.
>
> Only now does the Warden hand back: a diff, a passing test, and a note that the metrics
> exporter needs a follow-up. It does not hand back three Wright transcripts. It hands
> back one inspected result, and it tells the Archwright about the exporter, because that
> is a decision above the Guild's pay grade.

**Owns:** task decomposition within its craft, assignment, inspection and rework,
one clean handback, flagging anything that exceeds the Guild's remit.
**Does not:** reinterpret the task's intent, negotiate directly with the developer, or
pass unexamined work upward.

---

## Wright — the worker

A Wright does one scoped piece of work and returns the result. It is given a narrow brief
and the tools of its Guild, and it is deliberately kept ignorant of everything else: the
wider plan, the other Wrights, the developer's original words. That ignorance is a feature
— it is what lets a Wright be spawned cheaply, run in parallel with its peers, and be
thrown away or re-run without consequence.

The flip side is that a Wright will confidently do the wrong thing if its brief is wrong,
and will not notice, because it has no vantage point from which to notice. Everything a
Wright must respect has to be *in the brief*. A constraint the Warden knows but did not
write down is a constraint that will be violated. This is the single sharpest design
pressure in the whole factory, and it sits on the Warden.

> **A story.** A Wright receives: *in `src/limiter/bucket.ts`, change `refill()` to compute
> tokens from elapsed time since the last refill rather than from tick count. Preserve the
> configured rate exactly. Do not rename or add any exported symbol or config key. Return
> the diff.*
>
> It reads the file, makes the change, runs the unit tests for that module, and returns the
> diff. It does not ask why. It does not look at the metrics exporter, or wonder whether the
> developer would prefer a different algorithm, or fix the unrelated bug it noticed two
> functions down — though it does mention that bug in a line at the end of its result,
> because noticing is cheap and acting is not its call. Then it ends. Nothing it learned
> survives, which is fine: everything worth keeping is in the diff and that closing line.

**Owns:** one piece of work, done to the brief, returned in full.
**Does not:** expand its own scope, coordinate with peers, decide what happens next, or
assume context it was not given.

---

## One job, end to end

1. The developer tells the **Archwright** the rate limiter drops requests under burst.
2. The Archwright plans three steps and sends step one to the **Survey Guild**.
3. Survey's **Warden** splits it, dispatches **Wrights** to trace the code and read the
   logs, inspects what returns, and hands back one diagnosis with evidence.
4. The Archwright records the diagnosis and issues a precise change task to the
   **Masonry Guild**.
5. Masonry's **Warden** splits it, dispatches **Wrights** in parallel, rejects one
   result that broke a constraint, reissues it, and hands back a diff, a passing test, and
   a flagged follow-up.
6. The Archwright composes a single report and raises the one decision the developer
   actually has to make.

The developer spoke once and heard once. Everything between was the Archwright's.

---

## Open questions for the spec

These are deliberately unresolved — they need deciding, not assuming.

- **Guild roster.** Which Guilds exist at launch? Masonry and Survey are given. Do
  Hallmark and Docs start as Guilds, or as standards enforced inside Masonry?
- **Standing vs. summoned.** Is a Guild a long-lived process with warm context, or
  constituted per task? Does a Warden persist across tasks and accumulate knowledge of the
  codebase?
- **Rejection limits.** How many rework rounds may a Warden run before escalating to the
  Archwright? What does an unfixable piece look like coming back?
- **The brief contract.** What must a Warden put in every Wright brief? This is the
  highest-leverage artifact in the system and deserves a schema, not a convention.
- **Cross-Guild findings.** The metrics-exporter note surfaced inside Masonry but
  belongs to the whole job. How does a Warden hand up something that is not part of its
  deliverable?
- **Parallel Guilds.** May the Archwright run two Guilds at once, and if so, how does it
  prevent two Wardens' Wrights from touching the same files?
- **What the developer may see.** Strictly the Archwright's report, or can they look down
  into a Guild when they want to? Observability and the single-voice promise pull against
  each other.
- **Naming the plural.** A Guild's membership, collectively — "the Masonry Guild's
  wrights" works in prose but may want a term of its own for the API.
