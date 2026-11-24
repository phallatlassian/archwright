# Archwright

An agent-driven software factory: one developer-facing agent routes work through standing Guilds of specialist agents.

## Language

**Archwright**:
The single agent the developer talks to: one for the whole factory, an interactive claude or codex Session that the Works spawns and owns. Holds the plan, routes work to Guilds, reports back in one voice.
_Avoid_: orchestrator, main agent, overseer, the daemon, the CLI

**The Works**:
The factory's long-running daemon: holds Jobs, runs Guilds, serves the dashboard, and exposes the factory to the Archwright. Not an agent.
_Avoid_: server, backend, the Archwright

**Site**:
A git repository, registered with the Works by remote URL, whose project-level configuration the developer has chosen to trust. The Works keeps its own clone of it and never touches the developer's checkout (ADR 0019). A Job belongs to exactly one Site.
_Avoid_: Project, repo, workspace

**Forge**:
The service that hosts a Site's repository, issues and pull requests (GitHub, today). Agents may reach it directly, as the developer, under the developer's own permissions; the Works reads its state itself.
_Avoid_: remote, host, provider

**Runner**:
A vendor agent CLI (claude or codex) the Works can run an agent on, as an interactive Session, reached through an adapter.
_Avoid_: backend, provider, engine

**Guild**:
A standing body responsible for one kind of work, owning that craft's standards, tools and definition of finished.
_Avoid_: department, team, agent type

**Warden**:
A Guild's lead, standing for the Job: one per Guild, carrying out that Guild's steps in turn. Accepts a task from the Archwright, splits it, dispatches Wrights, and inspects what returns before it leaves the Guild.
_Avoid_: manager, supervisor, lead agent

**Wright**:
A worker that does one scoped piece of work from a brief and returns the result, knowing nothing of the wider plan.
_Avoid_: worker, subagent, sub-agent

**Session**:
A live interactive claude or codex process, hosted in a tmux pane, that carries out a run.
_Avoid_: process, terminal, pane

**Nudge**:
The Works delivering text to an idle Session.
_Avoid_: ping, poke, interrupt

**Job**:
One developer request, followed end to end through the factory.
_Avoid_: task, run, ticket

**Plan**:
The task graph the Archwright holds for one Job: steps for Guilds, with blocking edges and human checkpoints.
_Avoid_: workflow, pipeline

**Step**:
One unit of a Plan: work for a named Guild that the Archwright starts and the Guild's Warden carries out, with blockers, an optional human checkpoint, and a status. A Job has no stage that is not a step. A step sent back for rework is run again as another attempt, and the Plan stays as approved (ADR 0021).
_Avoid_: stage, phase, task

**Jig**:
A predescribed, reusable template for a recurring kind of Job (fix, build, specify), instantiated into a Job's Plan. A Jig is a prebuilt Plan; an arbitrary Job gets an ad hoc Plan.
_Avoid_: flow, route, playbook, recipe

**Writ**:
A ticket that authorises work; no Wright touches code that no writ authorised.
_Avoid_: work order

**Piece**:
One scoped unit of a Job's work that a Warden dispatches to a Wright, and may send back for rework before accepting.
_Avoid_: subtask, chunk

**Brief**:
The schema-checked instructions a Warden gives a Wright for one piece; the Works rejects an incomplete one and adds the Guild's standards.
_Avoid_: prompt, ticket

**Needs-you inbox**:
The Works' list of items that want the developer's eye without stopping work, such as a warning that two Jobs on one Site declared overlapping scopes.
_Avoid_: notifications, alerts

**Handback**:
The one inspected result a Warden returns to the Archwright: done, partial or failed, with any flags.
_Avoid_: report, response

**Gate run**:
The Works running a Site's registered gate commands (type-check, lint, test) on a snapshot of work in a throwaway checkout, outside any agent. The one on handback is the authoritative check; agents run the Site's checks themselves as they work.
_Avoid_: CI, test run, check

**Snapshot**:
A throwaway copy of a Wright's worktree, tracked and untracked files, that the Works takes so a gate run never touches the Wright's tree or git directory.
_Avoid_: checkout, backup

**Review**:
A Plan step, started by the Archwright after Masonry's handback and run by a read-only Warden that shares no context with the builder. It reports Standards and Spec independently as pass, fail or not evaluated, with findings tied to file and line. One fail sends the Job back to Masonry once, automatically; a second parks it. The PR go-ahead needs an overall pass (ADR 0012).
_Avoid_: code review, QA, approval

**Ask**:
A structured request for a human, raised by a Warden or Wright: questions with options and a recommended default, a kind (decision, manual step or clarification), and whether it blocks. The Works routes it up (Wright to Warden to Archwright to developer); the asker's Session stays open and the answer is Nudged in, with a fresh run from the Q&A record only if the Session is gone. The first answer wins; an Ask never expires.
_Avoid_: question, prompt, notification
