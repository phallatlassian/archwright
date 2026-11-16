# What if `review` and `open-pr` were injected into Plans instead of defined in them?

Researched 2026-11-16 against `main` at 398a70b plus the uncommitted ADR 0021 draft (`docs/adr/0021-every-job-stage-is-a-plan-step-and-review-is-one.md`) and its edits to ADRs 0006, 0008, 0009, 0012, 0020 and `GLOSSARY.md`. Facts from this repo's code and ADRs only. Nothing was run; behaviour claims come from reading code. Citations are file paths plus symbols. Where code does not exist yet (`start_step`, step-keyed handbacks, `result:` on Guilds) the claim is about the ADR text, and says so.

## Sources
- ADRs 0004, 0006, 0007, 0008, 0009, 0010, 0011, 0012, 0013, 0020, 0021 (draft); `GLOSSARY.md` (draft, with the new **Step** entry); `docs/research/review-as-a-plan-step.md`.
- Code: `internal/defs/{defs,parse,bind}.go`, `internal/defs/shipped/jigs/*.yaml`, `internal/defs/shipped/guilds/*/GUILD.md`; `internal/store/jobs.go` (`Step`, `Plan`, `setPlan`, `DecidePlan`, `ValidateSteps`, `Plan.Cleared`); `internal/works/{tools,definitions,dispatch,review,handback,gates,stepstate}.go`; `internal/sites/{worktrees,gatecheckout}.go`; `internal/works/ui/app.js`.
- GitHub issues #47 (`start_step`) and #48 (Warden runs) via `gh issue view`, for context only; #50 not re-read (its content is `docs/research/review-as-a-plan-step.md`).

## 0. Starting state in code

- `start_step` does not exist: no match in `internal/` (ADR 0020 and #47 describe it). Today `build.yaml` and `fix.yaml` end with `review` (guild hallmark, `blocked_by` the build step); neither has `open-pr` (ADR 0021 adds it). Nothing enforces `BlockedBy` (research doc section 4, Q1).
- `set_plan` (`catalogue["set_plan"]` in `internal/works/tools.go`) calls `Store.SetPlan` directly with the Archwright's `[]store.Step`. `start_jig` (`startJig`, `instantiateJig` in `definitions.go`) calls `Jig.Instantiate` then `Store.SetJigPlan`. Both reach `Store.setPlan`.
- `store.Step` is `{ID, Guild, Title, BlockedBy, Checkpoint, Brief}` (`internal/store/jobs.go`). It carries no inputs, outputs, access or kind.
- `ValidateSteps(steps []Step)` takes no Guild catalogue. It checks required fields, unique ids, known blockers and cycles only. It does not check that a Guild exists (issue #47 calls this #45).

## 1. What signal says "this step produces changes that must be kept beyond the Job"?

### 1.1 Candidate signals and where each is known

| Signal | Where it lives | Known when | Verdict |
|---|---|---|---|
| Guild access `read-write` | `defs.Guild.Access` (`defs.go`); the manifest default is `ReadOnly` when unset (`parse.go`, "the narrowest"). Only `masonry` ships `read-write`; `chancery`, `hallmark`, `survey` ship `read-only` (`shipped/guilds/*/GUILD.md`). | Plan-set time, by looking the step's `Guild` up in the registry (`Registry.Guild`). | Derivable now, static. Necessary but not sufficient. |
| Bound access | `defs.Binding.Access`, `DetachedWorktree` from `defs.Bind(g, runner, profiles)`: narrows to `ReadOnly` when the developer's runner profile is read-only. | Only when a run is bound. `Bind` has no caller outside tests (grep of `internal/`), so the Works does not bind today; the profiles come from runner adapters (#48/#51). | Exact, but not available at set time and not wired. |
| Worktree kind | `store.KindIntegration / KindWright / KindReadOnly` (`store/sites.go`); read-only Guilds get `readonly` detached worktrees (ADR 0009, `Binding.DetachedWorktree`). `CreateWorktree` allows one active integration worktree per Job and names its branch `archwright/<job>/integration` (`sites/worktrees.go`). | When a worktree is created. Today that is the HTTP `POST /jobs/{id}/worktrees` and `startReview`, not dispatch (research doc row 7). | Confirms "a Job has a keep-worthy branch" once a Wright exists. Not a Plan-time signal. |
| Site link | `Job.Site` (`store/jobs.go`); `startReview` refuses a Job with no Site; `jobSite` in `sites` needs one. | Job creation. | Necessary: no Site, no worktree, nothing to keep. A cheap guard against injecting for Site-less Jobs. |
| Writ link | `Job.Writ`, set by `link_writ` (`review.go`). | Any time, optional. | Not a signal. A writ authorises work; it says nothing about whether the result is kept. The Spec axis falls back without it (ADR 0012, `specMaterial`). |
| Jig metadata | `defs.JigStep.Outputs` (for example `outputs: [diff]` in `build.yaml`, `fix.yaml`) and `Inputs` (`build.diff`). | Jig load time. | Free-text labels. `Jig.validate` does not check them; `plan()` only appends them to the step's brief text (`parse.go`). `store.Step` has no outputs, so an ad hoc Plan has none. Not usable as a signal without a schema. |
| Handback result pointers | `HandbackInput.Results []string` (`handback.go`), "pointers to the accepted results". | Handback time. | Untyped strings; nothing says "diff". Not usable. |
| Actual diff | Git in the Works' clone. `IntegrationTip` (`sites/gatecheckout.go`) gives branch and SHA; `merged()` in `worktrees.go` already counts a branch's own commits with `rev-list --count <branch> --not <BaseSHA>`. | Any time after a Wright commits; the authoritative answer at handback. | Derivable now with a small helper, dynamic. |
| A declared flag | None. Nothing in `Guild` or `Jig` says "produces repo changes". `decodeStrict` uses `KnownFields(true)`, so an unknown manifest key fails the load (`parse.go`); a new field needs code. | n/a | Would be new (option D below). ADR 0021 already adds one optional manifest field, `result`. |

So the one signal available today at Plan-set time is `Guild.Access == ReadWrite` of each step's Guild, joined with `Job.Site != ""`. The one exact signal is the git diff, available only after work lands.

### 1.2 Static (Plan-set) versus dynamic (handback) triggering

Static, from Guild access, at `set_plan` / `start_jig`:
- Over-triggers. A read-write Guild step can end with no commits (Masonry decides the defect is not reproducible; a Wright fails; a piece is rejected). The injected `review` then has nothing to review and `open-pr` nothing to open. A no-op needs a defined end state ("skipped, nothing to keep"), or the Job hangs on steps that can never be started. This is the common failure of the static approach.
- Over-triggers on a bound read-only profile: `Bind` can narrow a read-write Guild to `ReadOnly`, so static `Guild.Access` is an upper bound of `Binding.Access`.
- Over-triggers on the PR step itself if `open-pr` runs in Masonry (ADR 0008: "the Masonry Warden itself pushes and opens the PR"): `masonry` is `read-write`, so an injector keyed on access would inject review and PR for the PR step. The injector must mark its own steps and skip them.
- Under-triggers on read-only Guilds whose output must be kept. Chancery is `read-only` and returns a spec or tickets by pointer (`GUILD.md` handback text); filing tickets is not a repo change and is outside Review's scope in v1 (ADR 0012: "Chancery's specs and tickets ... are not reviewed"). Survey is `read-only` and returns findings with evidence by pointer (ADR 0013). A static `read-write` rule correctly ignores both, and no code path in this repo writes a Survey note into the Site. Whether Survey findings should ever be committed to a Site is not decided anywhere I read (ADR 0013 mentions a docs Wright brief; Not verified below).
- Under-triggers on a Plan with no code step at plan time that later needs one (the Archwright re-plans). Re-planning calls `set_plan` again, so the rule re-runs; no loss.
- Strength: everything is known when the developer approves, so the approved Plan is complete, the dashboard shows every step, and `blocked_by` edges are computed once.

Dynamic, at handback of a step (or of the build step):
- Exact: inject only if `rev-list --count` on the integration branch against its base is non-zero.
- But the Plan grows after approval (see section 2.2), the dashboard graph changes under the developer, and the injection happens in a handback path (`handback.go`, which already triggers `startGates`) that has no Plan-editing concept. `Store.setPlan` replaces the whole Plan and resets approval for an ad hoc Plan (`ON CONFLICT ... approval = excluded.approval`); there is no append.
- Failure modes: a handback whose diff is empty but that left an untracked file; a handback `partial` (park, `ParkJob`) that has a diff; a Wright branch not yet merged into the integration branch (research doc row 11: no merge code found, so "the diff" may be on a Wright branch the integration branch has not received).

Hybrid worth noting: static injection (so the Plan is whole and approved), with a dynamic skip (a diff check when the step is due), so over-triggering degrades to a visible `skipped` status rather than a stuck Job. The dynamic check is the same `rev-list` call.

### 1.3 The four named edge cases

| Case | Static (Guild access) | Dynamic (diff) |
|---|---|---|
| Read-write step that changes nothing | Injects; needs a skip rule | Does not inject; correct |
| Survey step writes a note | Does not inject (read-only, and `Bind` would give a detached worktree that is not kept, ADR 0009) | Not reachable: a detached worktree's changes are not on any branch |
| Chancery files tickets | Does not inject; tickets are in the tracker, not the Site | Same |
| Masonry step | Injects; the main case | Injects iff commits exist |

Masonry-only is also what the shipped catalogue gives: `access: read-write` appears in exactly one `GUILD.md`. A developer-added read-write Guild would be covered automatically by an access-keyed rule, and by nothing in a name-keyed rule (ADR 0009 forbids name-keyed policy: "no privileged code").

## 2. Who injects, and when?

### 2.1 Candidates

- The Archwright, by judgment, through a tool. This is what ADR 0021 and 0020 assume: the Archwright authors or instantiates the Plan and calls `start_step`. "Injection" then means a tool such as `set_plan` accepting a Plan without review, and nothing happening. Not injection at all; it is option A.
- The Works, mechanically, inside `set_plan` and `start_jig` before `Store.setPlan` stores the Plan. Placement matters: `ValidateSteps` is in `store` and has no catalogue; the registry is in `works` (`c.runs.Definitions()`) and `defs` imports `store`, so the check and the rewrite belong in `works` (`tools.go` `set_plan`, `definitions.go` `instantiateJig`), not in `store`.
- The Works, at plan approval (`DecidePlan` via the API in `api.go`). The developer would approve one Plan and the Works would persist another.
- The Works, at handback (`handback.go` next to `startGates`).

### 2.2 Consequences

Plan approval (ADR 0007, 0020).
- Injection at `set_plan` time, before storage: the developer approves the Plan as stored, including the injected steps. `DecidePlan` approves the Plan the developer saw (`seen` / `updated_at` guard). No reset and no bypass. The Archwright's `set_plan` return value (the stored `Plan`) shows it the added steps. This is the only point at which injection and approval compose cleanly.
- Injection at approval: `DecidePlan` today flips only `plans.approval` and requires the `updated_at` the developer saw; adding steps there would change what was approved. Rejected.
- Injection at handback: the Plan has already been approved. Two outcomes, both bad: reset approval to pending (ADR 0021 calls this "asking the developer for a go they already gave"), or add steps without a go. Review is read-only, so adding it needs no go for safety. `open-pr` pushes to a remote, an external effect. ADR 0008 says "the developer is not prompted" for the push, so the Plan approval and the Archwright's go-ahead are the checks; an injected `open-pr` the developer never saw would sidestep the first.
- Jig Plans need no approval (`ApprovalNotRequired`, `setPlan`), so Jig injection raises no approval question, but the developer then sees steps they did not read in the Jig file. Jig content is visible in the catalogue (`catalogueOf`).

The Plan graph on the dashboard (`internal/works/ui/app.js`).
- For a named Jig the board builds its columns from the catalogue definition (`layout`, `catalogue.jigs.find(...).steps`) and falls back to the first Plan only when the definition is gone. Injected steps would not be in the definition, so they would have no column for named-Jig lanes unless the UI merges them. That is a real, small UI change; I did not trace how `stairTrack` treats a step missing from `columns`. Ad hoc Plans use `sharedSteps` over the stored Plans, so injected steps there appear naturally. Not verified end to end.
- Step status today is `StepStates` (`stepstate.go`): it only labels a step "running" if a live dispatch of its Guild exists, "state not tracked yet" otherwise, and has no notion of blocked, skipped or attempts. It works per Guild, so an injected `hallmark` step would be labelled consistently with a hand-written one. ADR 0021's `steps` table would replace it.

`blocked_by` edges.
- One Job has one integration branch (`CreateWorktree` refuses a second `KindIntegration`), review reads `LatestHandback(jobID)` and `IntegrationTip(jobID)` (`startReview`), and the PR go-ahead is a Job-level field (`Job.PRGoAhead`, `GrantGoAhead`). So everything downstream of code is naturally one-per-Job, at the end of the integration branch. The rule that follows: inject one `review` blocked by every code-producing step (all steps that matched the trigger), and one `open-pr` blocked by `review`. One review/PR per Job, not per step.
- Per-step injection (a review after each code step) is not supported by the model as written: `LatestHandback` is Job-keyed (ADR 0021 changes this to step-keyed), `reviewStale` compares to "the latest handback", and a PR per step would need several branches, but there is one integration branch per Job.
- If code steps are independent (two Masonry steps with no edge between them), the injected `review` waits for both; that is the correct shape for one integration branch. It is a fan-in; the dashboard's `layers` draws fan-ins (`app.js` comment on `stairTrack`).
- If an ad hoc Plan already contains a Hallmark step and/or an `open-pr` step, the injector must detect and not duplicate. Detection by Guild (`hallmark`) is name-keyed policy; by `result: verdict` (ADR 0021's field) is not. For `open-pr` there is no defined handle yet: it is a Masonry step in ADR 0008/0021 with nothing marking it as the PR step (see section 3).

Attempts and rework (ADR 0021).
- ADR 0021 wants rework as attempts of fixed steps so the Plan stays as approved. Injected steps are fixed steps too, so attempts apply unchanged. The advantage over dynamic injection is that rework never edits the Plan: a failed review re-opens `build` as attempt 2. Nothing in rework needs injection.
- One consequence of mechanical injection: the injector must run only at set time. Re-running it after attempts exist, or on a replaced Plan, would re-inject. `Store.setPlan` replaces the Plan wholesale, so re-injecting on every `set_plan` is idempotent only if the injector first strips its own earlier steps or detects them.

ADR 0011 (fail closed, never widen) and "can an agent-authored Plan omit or suppress review/PR?"
- ADR 0011 is about runner permissions (the never-list in adapters). It does not mention review. Review-before-PR is ADR 0012's rule, and ADR 0021 keeps it as the PR go-ahead guard.
- What stops an unreviewed PR today: `prGoAhead` (`review.go`) refuses unless `LatestReview` is a reported overall pass and not stale; `prGoAheadStatus` lets the Warden check. But the push itself is done by the Masonry Warden under the runner's own permission rules (ADR 0008) and the go-ahead is read by the Warden through a tool (`prGoAheadStatus`); the Works does not stop `git push`. So today's "review gates the PR" is enforced only as far as the Warden's skill honours `pr_go_ahead_status`. This is true with or without injection. ADR 0021 makes `open-pr` a step started through `start_step`, so the Works can refuse to start it (check `Job.PRGoAhead` and `reviewStale`); that is the real enforcement, and it does not depend on injection.
- With injection absent, an agent-authored Plan can omit `review` and `open-pr`. Consequence: no PR is opened (`open-pr` is what pushes), so the code stays on a local integration branch. `Close` with outcome accepted keeps branches; discarded deletes them (`Manager.Close`). That is fail-closed on delivery but silently drops a Job's value; and if the Plan omits only `review` while including `open-pr`, `prGoAhead` still refuses, and `start_step` on `open-pr` would refuse, so the Job stalls rather than shipping unreviewed.
- Hence injection is a completeness property (no stranded work), not the safety property. The safety property is the guard on `open-pr` (and the go-ahead's pass-and-fresh rule), which holds under every option. An agent could also bypass Plans entirely: `planCleared` returns nil when a Job has no Plan (`dispatch.go`), so a Job with no Plan can still dispatch Wrights today. Injection at `set_plan` would not touch that path; only a Works rule on dispatch or on close would.

## 3. Effect on the Jigs and ADRs

What simplifies:
- `build.yaml` and `fix.yaml` shrink to one step; the shipped definitions no longer carry a `review` step whose Guild name (`hallmark`) is repeated in every Jig a developer writes. `Jig.validate` and the dashboard Jig board draw the shape from fewer nodes.
- Ad hoc Plans cannot forget review/PR; an Archwright authoring a Masonry-only Plan gets a complete one.
- One place states the policy ("code that is kept is reviewed and PR'd"), instead of every Jig and every ad hoc Plan.

What gets harder, or contradicts:
- ADR 0009 ("Chancery, Masonry, Survey and Hallmark ship as ordinary definitions with no privileged code"; a Jig is a plain definition of steps). An injector is Works code that reads Guild access and rewrites Plans. It is not Guild-name policy if keyed on `access` and `result`, but it makes Jig steps not equal to the stored Plan, so "a Jig is a prebuilt Plan" (GLOSSARY **Jig**) is false for built-ins: the instantiated Plan has more steps than the definition. Mitigation: injection is Works policy in the same sense as ADR 0012's verdict rules, which ADR 0021 already keeps as Works code attached to a result kind.
- ADR 0021 as drafted says the `build` and `fix` Jigs "end with `review` then `open-pr`" and that `open-pr` is "a step of the Jig". Injection changes that to "the Works adds them", and the jig-level `blocked_by`/`inputs` (`inputs: [build.diff]` in `build.yaml`) is lost; the `review` step would need its inputs wired by the injector (there is no schema for step outputs, section 1.1).
- ADR 0012: "The Fix and Build Jigs include a Review step; the Review Jig is Review alone." The `review` Jig (`shipped/jigs/review.yaml`) has a single Hallmark step with `subject` free text (a branch, PR or diff). Review of an external branch or PR has no preceding build step, so there are no code-producing steps and nothing to inject; the Hallmark step is the user's own. No contradiction: Hallmark is read-only, so the trigger does not fire. But the PR step must not be injected there either: the Job has nothing of its own to PR. Also, a Plan with both a user-written `review` and a code step needs the dedup rule above.
- ADR 0020: `start_step` refuses unless `blocked_by` steps have a handback. Injected steps are ordinary for it. No change. Plan-approval text ("what the developer approves") must say the approved Plan includes Works-added steps.
- ADR 0021: "Review ... attaches to step start and completion, keyed by Guild access and result kind, not by the name `hallmark`." Injection keyed on access matches this principle. But `result: verdict` does not exist in code yet (`Guild` has no such field, `decodeStrict` rejects unknown keys); the injector would need the review Guild to be identified as "the Guild that provides verdicts". With several Guilds declaring `result: verdict`, which one is injected? Needs a rule: a Site or Works config naming the reviewing Guild, or a Guild manifest flag ("default reviewer"). Open decision.
- `open-pr` has no defined Guild. ADR 0008: the Masonry Warden pushes and opens the PR with the `pr` skill. ADR 0021 makes it a step, presumably Masonry, read-write. A step injected as Masonry read-write would match its own trigger (section 1.2). The injector therefore needs a step marker (for example a `kind` or `system` field on `store.Step`) that the trigger ignores and the dashboard can label.
- GLOSSARY: **Plan** ("The task graph the Archwright holds for one Job: steps for Guilds, with blocking edges and human checkpoints") becomes "that the Archwright holds, plus steps the Works adds"; **Jig** ("A Jig is a prebuilt Plan") needs the qualifier; **Step** ("A Job has no stage that is not a step") stays true as long as injected steps are stored as steps.

Is an injected step still "a step"? Yes, if the injection writes it into the stored Plan (`plans.steps` JSON, `Store.setPlan`) as a normal `store.Step` with an id, a Guild, `blocked_by` and a brief. It then has status, attempts (ADR 0021 `steps` table), a dashboard node, a `start_step` entry, handbacks keyed by step. If instead the Works runs review and PR itself without a Plan entry (option C below as a purely internal action), it is not a step, it is the hidden stage ADR 0021 exists to remove. So the injection must be into the Plan, visibly.

## 4. Options

### A. Static Jig steps, as drafted in ADR 0021
Jigs list `review` and `open-pr`. Ad hoc Plans are written by the Archwright, which is instructed (launch instructions, issue #44) to include them.
- Pros: no new mechanism; Jig = Plan holds; no approval ambiguity; matches ADR 0009 and the draft ADR 0021 text; dashboard layouts come from the definition.
- Cons: ad hoc Plans can omit them (stranded work, section 2.2); each developer-written Jig repeats Guild names; `open-pr`'s guard has to live elsewhere.
- Code: `shipped/jigs/{build,fix}.yaml` gain `open-pr`; `Jig.validate` unchanged. Optional: a `ValidateSteps`-time warning or refusal for a Plan with a read-write step and no downstream verdict step.

### B. Works appends review and open-pr at set time (static)
In `works`, a function (say `withKeptWork(steps, reg, job)`) is called by `tools.go` `set_plan` and by `definitions.go` `instantiateJig` before `Store.SetPlan` / `SetJigPlan`. It adds `review` (blocked by every step whose Guild has `Access == ReadWrite`) and `open-pr` (blocked by `review`) when none exists, skipped for Site-less Jobs.
- Pros: the developer approves the full Plan (no approval problem); the injected steps are real steps (section 3); one review/PR per Job falls out of the one integration branch; ad hoc Plans cannot forget.
- Cons: over-injection when a read-write step changes nothing (needs a `skipped` end state in the ADR 0021 step model); needs a step marker and a dedup rule; the stored Plan differs from a Jig's definition; reviewer Guild selection needs a rule; the UI must merge injected steps into named-Jig columns; `set_plan` is no longer a pure store call, so the Archwright's Plan is changed behind its back (it sees the result in the returned `Plan`).
- Code: new function in `internal/works` (not `store`, because the catalogue is in `works`); `store.Step` gains a marker; `ui/app.js` layout merge; `defs` a way to name the reviewer Guild; ADR 0021, 0009, 0020 and glossary edits.

### C. Works injects at handback when a diff exists (dynamic)
In `handback.go` after a done handback, compute the diff on the integration tip (`sites.Manager.IntegrationTip`, a new rev-list helper). If non-empty and no review step exists, append `review` and `open-pr` to the Plan.
- Pros: exact; no empty reviews.
- Cons: edits an approved Plan, so either resets approval (contradicting ADR 0021) or adds an unapproved external effect; no Plan-append operation exists (`Store.setPlan` replaces and resets approval); the dashboard graph changes mid-Job; untracked or unmerged Wright-branch changes can give a false "no diff" (row 11 of the research doc); mixes handback validation with Plan authoring. Rejected.

### D. A Guild-manifest flag drives injection, with Jig opt-out
`GUILD.md` gains an optional field, for example `produces: repo-changes`, alongside `access` and the ADR 0021 `result`. The Works injects (as in B) for steps whose Guild declares it, and a Jig may carry `inject: false` (for the `review` Jig, or a developer-written local-only Jig).
- Pros: explicit and not tied to the access heuristic (a read-only Guild could opt in, a future read-write Guild that only edits scratch space could opt out); consistent with ADR 0009's "behaviour from definitions, not from names"; gives a place for a "this is the reviewer Guild" flag too.
- Cons: more manifest surface; `decodeStrict` means a new `Guild`/`manifest` field in `parse.go` and `defs.go`; a flag the developer sets wrongly is a silent policy hole (a Guild forgetting it has no review); the opt-out means a Jig can suppress review, which weakens the "non-optional" argument unless the PR guard stays in the Works.
- Code: `defs.Guild` and `manifest` fields, `parse.go` validation, B's injector, `Jig` field, ADR 0009 amendment.

### Mapping to the earlier question
Option B is the narrowest injection (static, trigger already derivable). D is B with the trigger made explicit. A and C are the poles.

## 5. Recommendation

Keep A (steps defined in Jigs and ad hoc Plans) and add a Works validation, not an injection: make the PR guard and a completeness check a Works rule.
1. `open-pr` is guarded in `start_step`: refuse unless `Job.PRGoAhead` is set and `reviewStale` is nil. This is the safety property, it holds under A, B and D, and it is already half-built (`prGoAhead`, `GrantGoAhead`, `reviewStale`).
2. `set_plan` validation in `works` (not `store`): if the Job has a Site and any step's Guild has `Access == ReadWrite`, and the Plan has no later step whose Guild declares `result: verdict`, return an error telling the Archwright to add review and open-pr. A validation error keeps the Archwright the Plan's author, leaves approval semantics untouched, is visible, and costs about the same as B's trigger code without B's side effects (marker, dedup, UI merge, reviewer-Guild choice, hidden Plan edits).
3. Skip case: the Archwright may state a reason for omission only when the developer's request said local-only; this is an open decision (below).

Why not B: it buys "cannot forget" at the cost of the stored Plan differing from the Jig and the Archwright's own `set_plan`, and the over-injection problem needs a new `skipped` status and a diff check anyway. If the maintainer prefers a guarantee over a validation error, B with a diff-based skip is the next choice, and D if the trigger heuristic proves too coarse.

Why not C: it conflicts with ADR 0021's rule that the approved Plan stays as is.

## 6. Open decisions for the maintainer

1. Is "kept changes must be reviewed and PR'd" a property the Works guarantees (validation or injection), or a convention the Archwright follows (A)? ADR 0021 as drafted chose the second.
2. Trigger: static `Guild.Access == ReadWrite`, bound access, a manifest flag (D), or a diff at step time? Does the signal need a `Job.Site`?
3. Is there a legitimate local-only Job (code kept on the integration branch, no PR)? Who may say so: the Jig, the Archwright, the developer at approval?
4. Which Guild is "the reviewer" and which is "the PR opener" when the Works needs to name one, given ADR 0009 forbids name-keyed policy? Is `open-pr` a Masonry step (ADR 0008) or a distinct read-write Guild?
5. Over-injection: when the build step ends with no commits, do review and PR end `skipped` (new status), fail, or park the Job?
6. Multiple code steps: confirm one `review` and one `open-pr` per Job at the end of the integration branch, not per step.
7. Does the ADR 0021 `steps` table need a marker for Works-added steps (shown differently on the dashboard)?
8. The no-Plan hole: `planCleared` allows dispatch for a Job with no Plan. Does the guarantee extend to that path?
9. Does the developer's approval cover Works-added steps (and the `open-pr` push) explicitly in the approval UI?

## Not verified
- `start_step`, step-keyed handbacks, the `result:` manifest field, `open-pr` as a Jig step and the `steps` table do not exist in code; claims about them are about the ADR text and issue #47.
- How `stairTrack` in `internal/works/ui/app.js` renders a Plan step missing from a Jig's definition columns.
- Whether anything in the Masonry Warden skill (`shipped/guilds/masonry/warden/SKILL.md`) already tells the Warden to call `pr_go_ahead_status` before pushing; I did not read it. Whether the real runners could technically stop a push is outside this repo's code.
- Whether a Wright branch is ever merged into the integration branch by the Works (research doc row 11 found none); the diff-based signals depend on it.
- Whether `defs.Bind` will be called with runner profiles at set time or step start (no caller exists; #48/#51).
- Whether Survey notes or Chancery specs should ever be committed to a Site (not decided in the ADRs I read; ADR 0013 mentions a docs Wright brief but I did not trace it).
- `gh issue view 50` and the full text of #44 and #45 were not read; their content is referred to by the research doc and #47.
