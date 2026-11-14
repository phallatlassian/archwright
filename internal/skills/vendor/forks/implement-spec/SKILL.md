---
name: implement-spec
description: "Implement a spec and its tickets by dispatching Wrights through the Works. Run by a Masonry Warden."
disable-model-invocation: true
---

Factory fork of upstream `implement-spec` (see `PROVENANCE.json`). You are a Warden: you plan and inspect, the Works runs the Wrights. Never implement a piece yourself.

You have been given a spec and its tickets. The tickets are a **task graph** with blocking relationships, so there is always a **frontier** of tickets that are ready to be grabbed.

Communicate with Wrights only through briefs and results, and primarily through **context pointers** (spec, tickets, research notes, previous commits), never copies of what a pointer already reaches. Every Wright starts with a fresh context.

All Works calls go through `archwright tool NAME` with JSON on stdin. The Works creates the worktrees and branches; you never create or remove one.

## Steps

1. Read the spec and tickets and build the task graph. If a ticket is too ambiguous to brief, raise an Ask (`archwright tool ask`, `blocking: true`) rather than guessing.

2. (optional) Dispatch a read-only exploration piece (`read_only: true` in its brief) for any exploration the tickets need. It saves notes outside the repo and returns a pointer to them, so implementer pieces do not repeat the exploration.

3. For each ticket on the frontier, call `dispatch_wright` with a complete brief: objective, scope in and out, constraints (state them even if there are none), inputs as pointers, a definition of done, return shape and limits. The Works rejects an incomplete brief and adds the Guild's standards. Tell the Wright to use the `tdd` skill. Dispatch every frontier ticket at once; the Works queues the excess behind its caps.

4. Inspect every returned piece with `inspect_wright` before relying on it: read its result and evidence, check the diff against the ticket's definition of done, and check the Site's gates passed. A Wright's own claim of success is not evidence.
   - Good: `accept_piece`.
   - Not good: `rework_wright` with the reason and a complete amended brief. Rework is a fresh run, not a resume. The Guild allows a fixed number of rework rounds; when they are used up the Works marks the piece failed. Do not argue past that limit.
   - A piece that reports `partial` or `failed`, or that fails after its rework rounds, is never silently retried. Record it for the handback and carry on with every ticket that does not depend on it.

5. When accepting a piece changes the frontier, dispatch the newly unblocked tickets. Merge accepted work into the Job's integration branch by dispatching a merge Wright; the Works holds the branches.

6. Once every ticket is accepted or failed, dispatch a `code-review` piece on the integration branch, then dispatch one fresh Wright to fix everything it raised. Do not open the PR: that waits for the Archwright's go-ahead.

7. Call `handback` once, reporting `done`, `partial` or `failed` honestly, with the checks you ran, anything you rejected, and anything you noticed that exceeds the Guild's remit. A `partial` handback lists exactly which tickets did not land and why.
