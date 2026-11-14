# Can the ReviewRunner be removed so every Job stage is a Plan step? (#50)

Researched 2026-11-14 against `main` at a6c56d7. Facts from this repo's code and ADRs only; no external sources were needed. Citations are file paths plus symbols. Nothing was run against a real runner (none is wired, see section 2), so behaviour claims come from reading code and its tests.

## Sources
- Code: `internal/works/{review,dispatch,handback,gates,scheduler,api,skills,works,runs,fakereviewer}.go`, `internal/store/{reviews,dispatch,jobs,needsyou}.go` and `internal/store/migrations/`, `internal/defs/{defs,parse}.go`, `internal/defs/shipped/{jigs,guilds}/`, `internal/sites/worktrees.go`, `internal/runner/runner.go`.
- ADRs 0003, 0004, 0006, 0007, 0008, 0009, 0010, 0012, 0015, 0020; `GLOSSARY.md`; `docs/archwright-guilds.md`, `docs/archwright-asks.md`.
- The developer's live store (`archwright.db`, read-only): `reviews`, `handbacks`, `dispatches` and `gate_runs` all hold 0 rows, so no review, Wright or handback has ever run.

## 1. What ReviewRunner is, and is not

- `ReviewRunner` (`works.ReviewRunner`) is one method, `RunReview(ctx, ReviewRequest) error`. `ReviewRequest` is `{Env RunEnv, Job, ReviewID, Worktree, Brief}`.
- Its only caller is `(*Runs).startReview`, reached only from the Archwright tool `request_review` (`reviewTools` in `review.go`).
- `ConfigureReview` is called nowhere outside tests (`review_test.go`); `works.go` configures the Wright `Runner` (the fake) but not a reviewer. In production `request_review` therefore fails closed ("review runners arrive with the runner adapter").
- The interface does not carry the verdict. The verdict arrives as a separate tool call, `report_review` (`reportReview`), validated by `validateAxis` and `overallOf`, then acted on by `afterReview`.
- Everything that makes a review a review lives around the runner, in `startReview` (about 100 lines) and the `reviews` table, not in `RunReview`. The runner is only "launch an agent here with this brief".

## 2. Where review differs from every other run

| Aspect | Wright dispatch | Review run | Source |
|---|---|---|---|
| Start path | `dispatch_wright`, then `(*Runs).launch` and the scheduler | `request_review`, then `startReview` and its own goroutine | `dispatch.go`, `review.go` |
| Runner interface | `works.Runner.Run(ctx, RunRequest)` | `works.ReviewRunner.RunReview(ctx, ReviewRequest)` | `dispatch.go`, `review.go` |
| Run context | `RunContext{Job, Guild, DispatchID}` | `RunContext{Job, Guild, Review}` | `runs.go` |
| Tool role | Warden role for task tools; `warden()` rejects a run with `ctx.Review` set | Warden role, but only `report_review` is allowed | `dispatch.go` `(*callCtx).warden`, `reportReview` |
| Workspace | worktree of kind `wright` (created through `POST /jobs/{id}/worktrees`, see section 5) | detached `read-only` worktree created inside `startReview` | `sites/worktrees.go` `CreateWorktree`, `review.go` |
| Brief | `parseBrief` + `Brief.Render` | `renderReviewBrief` + `specMaterial` | `brief.go`, `review.go` |
| Record | `pieces`, `dispatches` | `reviews` | `migrations/0003`, `0008` |
| Scheduling | scheduler: caps and rate-limit pause | none; a bare goroutine counted only by `ActiveRuns` | `scheduler.go` (`entry.d store.Dispatch`), `review.go` |
| Skills | `deliverSkillsFor` (only on the dispatch path) | none; `ReviewRequest` has no `ExtraEnv` | `dispatch.go`, `skills.go` |

What the vendor runner sees is identical: `runner.Runner.Start(ctx, StartRequest{Brief, Dir, Limits, Env, Spawn})` (`internal/runner/runner.go`). The differences above are data (workspace kind, brief, tool role, record) and policy (scheduling, skills), not how a process is spawned. Nothing in the runner contract justifies a separate review interface.

Two related findings, neither specific to reviews:
- `UseSkills` has no caller outside its definition in `skills.go`, so `deliverSkillsFor` returns nil in production: even Wrights get no delivered skills today.
- The scheduler holds only dispatch entries (`entry{d store.Dispatch, ...}`), so reviews, and any task Warden run in #47, sit outside the caps of ADR 0008 and outside `show_queue` unless #47 or #48 changes that.

## 3. ADR 0009 already says Hallmark has no privileged code; the code disagrees

ADR 0009: "Chancery, Masonry, Survey and Hallmark ship as ordinary definitions with no privileged code". The Hallmark `GUILD.md` declares its handback as "A verdict on two axes, Standards and Spec ... with findings tied to file and line", so the Guild definition already treats the verdict as Hallmark's handback. But review.go hard-codes the Guild (`const ReviewGuild = "hallmark"`, used in `startReview` and `specMaterial`), the rework limit (`MaxReviewReworks`) and a verdict schema. So the privileged code is the thing #50 asks about, not a mere implementation detail.

## 4. Answers to the six questions

### Q1. Can a review be an ordinary step?

Yes, in principle. The `build` and `fix` Jigs already contain a `review` step with `guild: hallmark` and `blocked_by` the build step (`internal/defs/shipped/jigs/{build,fix}.yaml`). Today that step is decorative, and three code facts show it:
- `specMaterial` skips Plan steps whose Guild is `hallmark`, so nothing reads the step.
- Nothing enforces `BlockedBy` at all: it is validated by `store.ValidateSteps` and then never used (no other reference in `internal/` outside `defs/parse.go` copying it).
- The Jig `review` has a different intake (`subject`: "a branch, PR or diff"), but `startReview` can only review the Job's own integration branch after a Masonry handback (`LatestHandback`, `sm.IntegrationTip`). ADR 0012 says Hallmark may review "an external branch or PR"; the code cannot.

Where each responsibility of `startReview` would go:

| `startReview` responsibility | Becomes | Needs |
|---|---|---|
| Job not parked/closed/paused | generic `start_step` precondition (already in #47) | nothing new |
| Latest handback exists, is `done` | blocker steps have a `done` handback | handbacks keyed by step (see below) |
| Handback's gate run did not fail | generic rule: a blocker whose gate run failed does not unlock | gate runs already keyed by handback |
| No review already running | generic: one running attempt per step | step run record |
| Rework issued then new handback landed | generic: a re-opened step waits for its blocker's new handback | attempts, see Q4 |
| `IntegrationTip`, pin SHA, create detached worktree | step-start workspace preparation, driven by the Guild's access (`Binding.DetachedWorktree` exists, ADR 0009) | record the SHA on the step run |
| `renderReviewBrief` + `specMaterial` | the generic task brief of #47, with the Guild charter supplying the Standards/Spec wording and declared inputs supplying the writ, step briefs and request | see risk "independence" below |
| `NewReview` clears `pr_go_ahead_at` | the PR step's precondition is re-checked at use | none |

One structural gap blocks the "handback done" and "gate failed" rules as written: `handbacks`, `gate_runs`, `pieces` and `dispatches` are keyed by `job_id` (and guild or piece), never by Plan step (`migrations/0003`, `0005`; `LatestHandback(jobID)`). `startReview` reads "the latest handback" and messages "Masonry has not handed back", but it cannot tell a Masonry handback from a Survey or Chancery one on the same Job. #47 must key handbacks and gate runs by step.

### Q2. One runner path

A single contract is enough if the request carries what differs: the Guild binding (runner, access, denied tools; `defs.Binding`), the role, the workspace (path and kind), the brief, the environment (run env plus delivered-skills env) and limits. Wright, task Warden and review Warden then differ only in role, workspace kind and brief. No requirement was found that needs a separate `ReviewRunner`. Costs of keeping it: two interfaces and two fakes (`FakeRunner`, `FakeReviewer`), a second start path that skips the scheduler and skills delivery, and a second implementation in #48 for a stage that has never run.

### Q3. The verdict schema

A handback and a verdict are different shapes today. `HandbackInput.Validate` demands `checked`, `results` pointers, a done/partial/failed status and at least one dispatched piece ("nothing was dispatched for this Job"); a Hallmark Warden that reviews itself dispatches no Wrights (its `warden/SKILL.md` says Wrights "if useful"). `report_review` has axes, findings and pass/fail/not_evaluated.

Options, not mutually exclusive:
- A. Keep `report_review` as the step-result tool for Guilds that declare a verdict result. Lowest change, but keeps a second result path.
- B. Let a Guild manifest declare a result kind (for example `result: verdict`) and have the one handback tool accept that shape, relaxing the "pieces > 0" rule for Guilds that declare it. This matches `GUILD.md`'s existing `handback:` text and ADR 0009's "no privileged code". Recommended, with the shape of the field left to the ADR.

Wherever it lives, `validateAxis`, `overallOf` and `afterReview` are policy applied on completion of a step whose result is a verdict. They stay Works code (the Works enforces schemas, ADR 0006) but attach to a result kind, not to the Guild name.

### Q4. Rework as steps

Today (ADR 0012, `afterReview`, `issueReviewRework`, `Store.IssueRework`): a first fail records constraints on the review row and returns `next: issue_review_rework`; the Archwright calls it and is told to dispatch Masonry with the constraints. No code builds that Masonry task; it is the Archwright's job. A second fail parks the Job and files a Needs-you item.

Two ways to show this in the Plan:
- New step instances (`build-2`, `review-2`). Visible, but it rewrites the Plan, and `setPlan` resets approval to `pending` for an ad hoc Plan (`ON CONFLICT ... approval = excluded.approval`), re-asking the developer for a go they already gave.
- **Attempts** of a fixed step: the Plan stays as approved; each step has a run history (round 1, 2, ...), as pieces already do (`dispatches.round`, `UNIQUE(job_id, piece, round)`). A failed verdict re-opens the review's blocker (`build`) as attempt 2 with the findings as constraints, and the review as attempt 2 after it. Recommended: it keeps the Plan the Archwright owns static and approved, and reuses the round concept.

The "once automatically, then park" limit (`MaxReviewReworks`) becomes a Works rule on step attempts; whether the Works or the Archwright re-starts the build attempt can stay as in ADR 0020 (Archwright calls `start_step`, the Works fills the constraints into the brief).

### Q5. Audit: every stage or transition that is not a Plan step

Classification: **step** (should be one), **mechanism** (Works, no agent), **judgment** (Archwright), **inside a step** (belongs to a step's own run).

| # | Stage or transition | Where | Class | Reason |
|---|---|---|---|---|
| 1 | Create Job | `create_job` | judgment | Precedes any Plan. |
| 2 | Author or choose the Plan | `set_plan`, `start_jig` | judgment | It is the Plan. |
| 3 | Plan approval | `approve_plan`, `Store.DecidePlan` | mechanism | Human gate executed by the Works (ADR 0007). |
| 4 | Step checkpoint | `Step.Checkpoint`, Jigs `specify` and `map` | **step state, unenforced** | Stored and printed by the CLI, never enforced (no reference in `internal/works`). Should hold the step's blockers until the developer approves. |
| 5 | Link a writ | `link_writ`, `PUT /jobs/{id}/writ` | judgment | A Job attribute, not a stage. |
| 6 | Scope-overlap warning | `warnScopeOverlap` | mechanism | Side effect of dispatch. |
| 7 | Create integration and Wright worktrees | `POST /jobs/{id}/worktrees`; `CreateWorktree` has no caller in dispatch | **hidden mechanism** | Today only an HTTP endpoint and `startReview` call it; dispatch does not. Should be automatic step-start preparation. |
| 8 | Dispatch, queue, rate-limit pause | `dispatch_wright`, scheduler | inside a step | Pieces are units within a step (glossary: Piece). |
| 9 | Piece rework | `rework_wright` | inside a step | Warden-internal. |
| 10 | Handback and gate re-run | `handback`, `startGates` | mechanism | Deterministic, not agent work (ADR 0004, 0010). Surface its state on the step instead of hiding it. |
| 11 | Merge Wright branches into the integration branch | ADR 0008 "merge Wright" | **unresolved** | No merge code found (only `CleanupMerged`'s `merge-base --is-ancestor`) and no Jig step. Not verified as implemented. Most likely part of the build step's own work. |
| 12 | Review | `request_review` | **step** | The subject of this ticket. |
| 13 | Review rework | `issue_review_rework` | step attempt | Q4. |
| 14 | Park after partial handback or second failed review | `ParkJob`, `parked_reason`, Needs-you | mechanism | Expose as step status. |
| 15 | PR go-ahead | `pr_go_ahead`, `GrantGoAhead` | judgment, Works-enforced | Archwright decides; the Works already enforces "overall pass of the current code" (`reviewStale`). Keep as the guard on the PR step. |
| 16 | Push the branch and open the PR | ADR 0008 (Masonry Warden, `pr` skill); `pr_go_ahead_status` | **step (missing)** | No Jig step, and no run is defined to do it: Wardens are per task and end at handback (ADR 0004), while `pr_go_ahead_status` assumes a Warden alive after review. Not resolved by the code. |
| 17 | Ask: raise, escalate, answer, park, re-dispatch | `asks.go`, ADR 0015 | mechanism inside a step | Show as step status `blocked`; ADR 0020's status list lacks it. `docs/archwright-asks.md`: a Warden's blocking ask does not start a fresh run. |
| 18 | Needs-you items | `needsyou.go` | mechanism | Notifications, not stages. |
| 19 | Pause, resume, cancel Job, cancel Wright, delete, purge, prune | `api.go`, ADR 0007, 0017 | mechanism | Developer controls. |
| 20 | Instruct, retry, replan, change Jig | `judgment(...)` routes | judgment | Delivered to the Archwright's inbox. |
| 21 | Close Job as accepted or discarded | `POST /jobs/{id}/close`, `Manager.Close` | mechanism | Final developer transition; removes worktrees. |
| 22 | Final report to the developer | Archwright | judgment | The Archwright's one voice. |

So "no hidden stages" has exactly two real gaps beyond review: the PR (row 16) and worktree preparation (row 7); plus one unenforced concept (checkpoints, row 4) and one unresolved design item (merging, row 11).

### Q6. Impact

- **ADR 0012:** amend "the Archwright issues Hallmark as a separate task" to "the Archwright starts the `review` step". The independence rules (fresh Warden, read-only, no builder context), the verdict rules and the one-rework limit stay. The "Review" glossary entry changes the same way.
- **ADR 0020:** add statuses (`blocked` on an ask, `awaiting developer` for checkpoints, `needs rework` or attempts), key handbacks by step, and say what `start_step` does on a review step (this ticket's open question).
- **ADR 0009:** add the optional result kind to the Guild manifest.
- **ADR 0006:** a handback may carry a verdict result; the "pieces > 0" rule becomes Guild-dependent.
- **ADR 0008:** add an explicit PR step; say whether Warden runs count toward the caps.
- **GLOSSARY.md:** there is no **Step** entry (only the Plan entry mentions steps); add one.
- **Shipped Jigs:** add an `open-pr` step to `build` and `fix`, blocked by `review`.
- **Code removed:** `ReviewRunner`, `FakeReviewer`, `ConfigureReview`, `request_review`, `ReviewInfo`, most of `startReview`, and (after porting) the `reviews` table and `show_review`. Size: `review.go` 574 lines, `store/reviews.go` 402, `fakereviewer.go` 68, `review_test.go` 439. The review logic is exercised only by tests today, and the live store has zero reviews.
- **Dashboard:** `GET /jobs/{id}/reviews` and `Needs-you` kinds `review-parked` and `review-not-evaluated` become step-level views. The dashboard UI is not in this repo and was not checked.

## 5. Risks of removal

- **Independence.** ADR 0012 requires the reviewer to know nothing of how the work was built. A generic brief renderer must include only a step's declared inputs (as `build.diff` in the Jig), never the blocker's handback body or flags. Today independence is enforced by `renderReviewBrief` being a separate function; it would become a property of the input rules and needs a test.
- **Staleness.** `reviewStale` ties a pass to the reviewed SHA and latest handback. That check must remain on the PR step.
- **One review at a time and `InterruptReviews`.** Both generalise to step attempts and a restart sweep, which #47 already needs for running steps.
- **Two quirks of the shipped Jigs.** The `review` Jig's `subject` is free text and cannot drive a branch checkout; reviewing external branches needs a defined input form.

## 6. Recommendation

**Remove `ReviewRunner` and the separate `request_review` start path; keep review's policy.** The separate runner buys nothing (section 2), the separate path hides the stage from the Plan, the caps, skills delivery and the dashboard, and the code has never run in production, so this is the cheapest moment to remove it. What stays is the policy: read-only detached workspace pinned to a SHA, fresh Warden, verdict schema, one automatic rework, stale detection, go-ahead rule. These move to step start and step completion, keyed by Guild access and result kind, not by the name `hallmark`.

Amendments: see Q6. Changes to open issues:

- **#47 (`start_step`)** grows: handbacks and gate runs keyed by step; step attempts and statuses (`blocked`, checkpoint, rework); workspace preparation from Guild access, pinning the SHA; generic blocker rules (done handback, gates not failed); enforce `BlockedBy` and checkpoints; the review step is an ordinary step, which closes the open question already on the ticket. Consider splitting: (a) step state and `start_step` for non-review steps, as briefed now; (b) review as a step.
- **#48 (Warden runs)** shrinks to one interface, no review half. It should also fix skills delivery (`UseSkills` is never called) and decide whether Warden runs count toward the caps.
- **#51 (Wright runs)** is independent of this decision.

Suggested order:
1. Settle the ADR amendments above (0012, 0020, 0009, 0006, 0008) and add Step to the glossary.
2. #47 part (a), with step-keyed handbacks and attempts built in, so review does not need a later migration.
3. #51, in parallel.
4. Review as a step: port the verdict schema, `afterReview` rules, staleness and go-ahead guard; delete `ReviewRunner`, `FakeReviewer`, `request_review` and the `reviews` path.
5. PR step and Jig updates; enforce checkpoints.
6. #48, reduced, against the one interface.

## Not verified
- How the dashboard UI uses reviews (not in this repo).
- Whether the real runners can enforce a read-only Guild with a detached worktree (ADR 0009 says unsupported restrictions fail the bind; no adapter exercises it).
- Row 11, whether a merge Wright exists anywhere beyond ADR 0008's text; row 16, who runs the PR push.
- How a real Archwright session behaves with `request_review`; no real session has called it (zero reviews in the store).
