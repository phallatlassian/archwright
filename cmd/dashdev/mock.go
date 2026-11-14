package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/store"
	"github.com/phallatlassian/archwright/internal/works"
)

// The mock data is built from the Works' own response types, so a change to
// what the real API returns breaks this file's build instead of drifting
// silently. Times are relative to start-up, so "5m ago" always reads fresh.

// mockJob is everything the mock knows about one Job.
type mockJob struct {
	Job        store.Job
	Plan       *store.Plan
	Asks       []store.Ask
	Dispatches []store.Dispatch
	Runs       []store.RunRecord
	Reviews    []store.Review
	Worktrees  []store.Worktree
}

type mockWorld struct {
	mu        sync.Mutex
	Jobs      []*mockJob
	Sites     []store.Site
	NeedsYou  []store.NeedsYouItem
	Events    map[string][]store.RunEvent
	Catalogue works.Catalogue
	Queue     works.QueueStatus
}

func (w *mockWorld) job(id string) *mockJob {
	for _, j := range w.Jobs {
		if j.Job.ID == id {
			return j
		}
	}
	return nil
}

// state places a Job on the board the way works.Board does.
func (j *mockJob) state() string {
	live := 0
	for _, d := range j.Dispatches {
		if d.Status == store.DispatchQueued || d.Status == store.DispatchRunning {
			live++
		}
	}
	for _, r := range j.Reviews {
		if r.Status == store.ReviewRunning {
			live++
		}
	}
	blocked := false
	for _, a := range j.Asks {
		if a.Holder == store.HolderArchwright && a.Blocking {
			blocked = true
		}
	}
	switch {
	case j.Job.Outcome == "accepted":
		return works.BoardAccepted
	case j.Job.Outcome == "discarded":
		return works.BoardDiscarded
	case j.Job.ParkedFor != "":
		return works.BoardParked
	case blocked || (j.Plan != nil && j.Plan.Approval == store.ApprovalPending):
		return works.BoardNeedsYou
	case j.Job.PausedAt != "":
		return works.BoardPaused
	case live > 0:
		return works.BoardRunning
	case j.Plan == nil:
		return works.BoardPlanning
	}
	return works.BoardIdle
}

func (j *mockJob) live() int {
	n := 0
	for _, d := range j.Dispatches {
		if d.Status == store.DispatchQueued || d.Status == store.DispatchRunning {
			n++
		}
	}
	return n
}

func (j *mockJob) board() works.BoardJob {
	b := works.BoardJob{Job: j.Job, State: j.state(), LiveRuns: j.live(), OpenAsks: len(j.Asks)}
	if j.Plan != nil {
		b.Approval, b.Jig, b.Steps = j.Plan.Approval, j.Plan.Jig, len(j.Plan.Steps)
	}
	for _, r := range j.Runs {
		b.InputTokens += r.InputTokens
		b.OutputTokens += r.OutputTokens
		b.CostUSD += r.CostUSD
	}
	return b
}

func (w *mockWorld) usage() []store.UsageRow {
	type key struct{ job, guild, runner string }
	rows := map[key]*store.UsageRow{}
	var order []key
	for _, j := range w.Jobs {
		for _, r := range j.Runs {
			k := key{r.JobID, r.Guild, r.Runner}
			u := rows[k]
			if u == nil {
				u = &store.UsageRow{JobID: k.job, Guild: k.guild, Runner: k.runner}
				rows[k] = u
				order = append(order, k)
			}
			u.Runs++
			u.InputTokens += r.InputTokens
			u.OutputTokens += r.OutputTokens
			u.CostUSD += r.CostUSD
		}
	}
	out := []store.UsageRow{}
	for _, k := range order {
		out = append(out, *rows[k])
	}
	return out
}

func newWorld(scenario string) *mockWorld {
	w := &mockWorld{Events: map[string][]store.RunEvent{}}
	w.Catalogue = mockCatalogue()
	w.Queue = works.QueueStatus{Limits: works.Limits{Total: 4, PerSite: 3, PerWarden: 2}, Running: []works.QueueEntry{}, Queued: []works.QueueEntry{}}
	w.Sites = []store.Site{
		{Name: "archwright", URL: "https://github.com/phallatlassian/archwright", BaseBranch: "main", ConcurrencyCap: 3,
			Gates: store.Gates{Typecheck: "go vet ./...", Lint: "golangci-lint run", Test: "go test ./..."}, CreatedAt: ago(72 * time.Hour)},
		{Name: "billing-api", URL: "https://github.com/example/billing-api", BaseBranch: "trunk", ConcurrencyCap: 3,
			Gates: store.Gates{Test: "npm test"}, CreatedAt: ago(48 * time.Hour)},
	}
	if scenario == "empty" {
		return w
	}
	seed(w)
	return w
}

func ago(d time.Duration) time.Time { return time.Now().UTC().Add(-d) }
func tptr(t time.Time) *time.Time   { return &t }

func steps(spec ...string) []store.Step {
	// spec is (id, guild, title) triples; blockers are set by the caller.
	var out []store.Step
	for i := 0; i+2 < len(spec); i += 3 {
		out = append(out, store.Step{ID: spec[i], Guild: spec[i+1], Title: spec[i+2]})
	}
	return out
}

// buildPlan is the common Jig-shaped Plan: survey, then masonry, then hallmark review.
func buildPlan(jobID, jig, approval string, at time.Time) *store.Plan {
	s := steps("explore", "survey", "Trace the current code path",
		"implement", "masonry", "Implement the change",
		"review", "hallmark", "Review against the spec")
	s[1].BlockedBy = []string{"explore"}
	s[2].BlockedBy = []string{"implement"}
	s[2].Checkpoint = true
	return &store.Plan{JobID: jobID, Steps: s, Jig: jig, Approval: approval, UpdatedAt: at}
}

// investigatePlan is a Plan instantiated from the investigate-and-fix Jig.
func investigatePlan(jobID string, at time.Time) *store.Plan {
	s := steps("trace-code", "survey", "Trace the failing code path",
		"trace-docs", "survey", "Check docs and ADRs for intended behaviour",
		"plan-fix", "chancery", "Draft the fix as a ticket",
		"fix", "masonry", "Implement the fix with a regression test",
		"review", "hallmark", "Review against the ticket")
	s[2].BlockedBy, s[2].Checkpoint = []string{"trace-code", "trace-docs"}, true
	s[3].BlockedBy = []string{"plan-fix"}
	s[4].BlockedBy = []string{"fix"}
	return &store.Plan{JobID: jobID, Steps: s, Jig: "investigate-and-fix", Approval: store.ApprovalNotRequired, UpdatedAt: at}
}

func dispatch(id, jobID, piece, guild string, round int, status, runID string, at time.Time) store.Dispatch {
	brief, _ := json.Marshal(map[string]string{"piece": piece, "goal": "Mock brief for " + piece})
	return store.Dispatch{ID: id, JobID: jobID, Piece: piece, Guild: guild, Round: round, RunID: runID,
		Brief: brief, Rendered: "# Brief\nMock brief for " + piece + "\n\n## Guild standards\n- keep changes small\n",
		Status: status, CreatedAt: at}
}

func run(id, jobID, dispatchID, guild, runner string, in, out int, cost float64, status string, started time.Time, dur time.Duration) store.RunRecord {
	r := store.RunRecord{RunID: id, JobID: jobID, DispatchID: dispatchID, Guild: guild, Runner: runner,
		InputTokens: in, OutputTokens: out, CostUSD: cost, Status: status, StartedAt: started}
	if status != "running" {
		r.EndedAt = tptr(started.Add(dur))
	}
	return r
}

func runEvents(runID string, start time.Time, n int, prune bool) []store.RunEvent {
	kinds := []string{"message", "tool_call", "tool_result", "tool_call", "tool_result", "message", "usage"}
	sums := []string{"Reading the task and the surrounding code", "Read internal/works/dispatch.go", "412 lines", "Edit internal/works/dispatch.go",
		"Applied 1 edit", "Tests pass, writing the handback", "in=18,204 out=2,311"}
	var out []store.RunEvent
	for i := 0; i < n; i++ {
		out = append(out, store.RunEvent{RunID: runID, Seq: i + 1, Kind: kinds[i%len(kinds)], Summary: sums[i%len(sums)],
			HasRaw: !prune, At: start.Add(time.Duration(i) * 20 * time.Second)})
	}
	return out
}

func seed(w *mockWorld) {
	add := func(j *mockJob) { w.Jobs = append(w.Jobs, j) }

	// 1. Running: a Wright is live.
	j1 := &mockJob{Job: store.Job{ID: "j-7f3a21", Title: "Add retry with backoff to the webhook sender", Description: "Webhook deliveries fail permanently on a single 5xx. Retry with exponential backoff and jitter, capped at 5 attempts.", Site: "billing-api", Writ: "BILL-212", CreatedAt: ago(40 * time.Minute)}}
	j1.Plan = buildPlan(j1.Job.ID, "build", store.ApprovalNotRequired, ago(39*time.Minute))
	j1.Dispatches = []store.Dispatch{
		dispatch("d-101", j1.Job.ID, "explore", "survey", 0, store.DispatchReported, "r-101", ago(38*time.Minute)),
		dispatch("d-102", j1.Job.ID, "implement", "masonry", 0, store.DispatchRunning, "r-102", ago(6*time.Minute)),
	}
	j1.Runs = []store.RunRecord{
		run("r-101", j1.Job.ID, "d-101", "survey", "claude", 41200, 5200, 0.2841, "done", ago(38*time.Minute), 4*time.Minute),
		run("r-102", j1.Job.ID, "d-102", "masonry", "codex", 96300, 12800, 0, "running", ago(6*time.Minute), 0),
	}
	j1.Worktrees = []store.Worktree{
		{ID: "wt-1", JobID: j1.Job.ID, Site: "billing-api", Kind: store.KindIntegration, Path: "/home/dev/.archwright/work/billing-api/j-7f3a21", Branch: "aw/j-7f3a21", BaseRef: "trunk", BaseSHA: "a1b2c3d", CreatedAt: ago(39 * time.Minute)},
		{ID: "wt-2", JobID: j1.Job.ID, Site: "billing-api", Kind: store.KindWright, Path: "/home/dev/.archwright/work/billing-api/j-7f3a21-implement", Branch: "aw/j-7f3a21/implement", BaseRef: "aw/j-7f3a21", BaseSHA: "e4f5a6b", CreatedAt: ago(6 * time.Minute)},
	}
	w.Events["r-101"] = runEvents("r-101", ago(38*time.Minute), 7, false)
	w.Events["r-102"] = runEvents("r-102", ago(6*time.Minute), 12, false)
	add(j1)

	// 2. Needs you: an ad hoc Plan waits for approval.
	j2 := &mockJob{Job: store.Job{ID: "j-2c9d04", Title: "Refactor auth middleware to use the shared session store", Site: "billing-api", CreatedAt: ago(15 * time.Minute)}}
	j2.Plan = buildPlan(j2.Job.ID, "", store.ApprovalPending, ago(12*time.Minute))
	add(j2)

	// 3. Needs you: a blocking ask the Archwright escalated to the developer.
	j3 := &mockJob{Job: store.Job{ID: "j-91be55", Title: "Fix flaky scheduler test", Description: "TestSchedulerFairness fails about 1 run in 20 on CI.", Site: "archwright", CreatedAt: ago(2 * time.Hour)}}
	j3.Plan = investigatePlan(j3.Job.ID, ago(2*time.Hour))
	j3.Dispatches = []store.Dispatch{
		dispatch("d-301", j3.Job.ID, "trace-code", "survey", 0, store.DispatchReported, "r-301", ago(110*time.Minute)),
		dispatch("d-303", j3.Job.ID, "trace-docs", "survey", 0, store.DispatchReported, "r-303", ago(108*time.Minute)),
		dispatch("d-302", j3.Job.ID, "plan-fix", "chancery", 0, store.DispatchBlocked, "r-302", ago(30*time.Minute)),
	}
	j3.Runs = []store.RunRecord{
		run("r-301", j3.Job.ID, "d-301", "survey", "claude", 30100, 3900, 0.2012, "done", ago(110*time.Minute), 3*time.Minute),
		run("r-303", j3.Job.ID, "d-303", "survey", "claude", 18300, 2200, 0.1204, "done", ago(108*time.Minute), 2*time.Minute),
		run("r-302", j3.Job.ID, "d-302", "chancery", "claude", 35800, 4400, 0.2917, "done", ago(30*time.Minute), 6*time.Minute),
	}
	j3.Asks = []store.Ask{{
		ID: "ask-1", JobID: j3.Job.ID, Asker: "warden", Guild: "chancery", Piece: "plan-fix", DispatchID: "d-302", Kind: store.AskDecision,
		Blocking: true, Why: "Two fixes are viable and they differ in how much of the scheduler's public behaviour changes.",
		Questions: []store.Question{
			{ID: "q1", Text: "How should the test be made deterministic?", Options: []string{"Inject a fake clock", "Sleep longer", "Remove the timing assertion"}, Default: "Inject a fake clock"},
			{ID: "q2", Text: "Which extra checks should the Wright add?", Options: []string{"Race detector run", "Stress loop x100", "Benchmark"}, Multiple: true},
			{ID: "q3", Text: "Anything else the Wright should know?"},
		},
		Holder: store.HolderArchwright, Status: store.AskOpen, Answers: []store.Answer{}, CreatedAt: ago(25 * time.Minute),
	}}
	w.Events["r-301"] = runEvents("r-301", ago(110*time.Minute), 5, false)
	w.Events["r-302"] = runEvents("r-302", ago(30*time.Minute), 9, false)
	w.Events["r-303"] = runEvents("r-303", ago(108*time.Minute), 4, false)
	add(j3)

	// 4. Parked: two failed reviews.
	j4 := &mockJob{Job: store.Job{ID: "j-5d1e88", Title: "Upgrade the sqlite driver to v1.40", Site: "archwright", Writ: "AW-77", ParkedFor: "The second review failed: the migration breaks WAL checkpointing on startup.", CreatedAt: ago(5 * time.Hour)}}
	j4.Plan = buildPlan(j4.Job.ID, "build", store.ApprovalNotRequired, ago(5*time.Hour))
	j4.Dispatches = []store.Dispatch{
		dispatch("d-401", j4.Job.ID, "implement", "masonry", 0, store.DispatchRejected, "r-401", ago(4*time.Hour)),
		dispatch("d-402", j4.Job.ID, "implement", "masonry", 1, store.DispatchReported, "r-402", ago(3*time.Hour)),
	}
	j4.Runs = []store.RunRecord{
		run("r-401", j4.Job.ID, "d-401", "masonry", "claude", 88000, 14100, 0.9120, "done", ago(4*time.Hour), 11*time.Minute),
		run("r-402", j4.Job.ID, "d-402", "masonry", "claude", 61000, 9800, 0.6234, "done", ago(3*time.Hour), 8*time.Minute),
		run("r-403", j4.Job.ID, "", "hallmark", "claude", 22000, 2100, 0.1411, "failed", ago(2*time.Hour), 2*time.Minute),
	}
	failing := func(seq int, at time.Time, reason string) store.Review {
		return store.Review{ID: fmt.Sprintf("rv-%d", seq), JobID: j4.Job.ID, Seq: seq, HandbackID: int64(seq), Branch: "aw/j-5d1e88", SHA: "9c8b7a6", RunID: "r-403",
			Status: store.ReviewReported, SpecBasis: store.SpecWrit, Brief: "Review the integration branch.", Overall: store.VerdictFail,
			Standards: &store.Axis{Verdict: store.VerdictFail, Reason: "Two standards violations", Findings: []store.Finding{
				{File: "internal/store/store.go", Line: 88, Severity: store.SeverityMajor, Reason: reason},
				{File: "internal/store/store.go", Line: 140, Severity: store.SeverityMinor, Reason: "Error is wrapped without context"}}},
			Spec:      &store.Axis{Verdict: store.VerdictPass, Reason: "Matches AW-77", Findings: []store.Finding{}},
			CreatedAt: at, FinishedAt: tptr(at.Add(2 * time.Minute))}
	}
	j4.Reviews = []store.Review{failing(1, ago(3*time.Hour), "WAL checkpoint is skipped on startup"), failing(2, ago(2*time.Hour), "Migration breaks WAL checkpointing on startup")}
	w.Events["r-401"] = runEvents("r-401", ago(4*time.Hour), 8, true)
	add(j4)
	w.NeedsYou = append(w.NeedsYou, store.NeedsYouItem{ID: 1, Kind: store.NeedsReviewParked, JobID: j4.Job.ID, Site: "archwright",
		Summary: "A second failed review parked the Job.", Detail: json.RawMessage(`{"reviews":["rv-1","rv-2"],"blockers":1}`), CreatedAt: ago(2 * time.Hour)})

	// 5. Accepted.
	j5 := &mockJob{Job: store.Job{ID: "j-0aa1c2", Title: "Document the ADR workflow", Site: "archwright", Outcome: "accepted", CreatedAt: ago(26 * time.Hour)}}
	j5.Plan = buildPlan(j5.Job.ID, "build", store.ApprovalNotRequired, ago(26*time.Hour))
	j5.Dispatches = []store.Dispatch{dispatch("d-501", j5.Job.ID, "implement", "masonry", 0, store.DispatchReported, "r-501", ago(25*time.Hour))}
	j5.Runs = []store.RunRecord{run("r-501", j5.Job.ID, "d-501", "masonry", "claude", 20400, 3100, 0.1522, "done", ago(25*time.Hour), 5*time.Minute)}
	j5.Reviews = []store.Review{{ID: "rv-5", JobID: j5.Job.ID, Seq: 1, Branch: "aw/j-0aa1c2", SHA: "1f2e3d4", Status: store.ReviewReported, SpecBasis: store.SpecPlanAndRequest,
		Overall: store.VerdictPass, Standards: &store.Axis{Verdict: store.VerdictPass, Findings: []store.Finding{}}, Spec: &store.Axis{Verdict: store.VerdictPass, Findings: []store.Finding{}}, CreatedAt: ago(24 * time.Hour)}}
	w.Events["r-501"] = runEvents("r-501", ago(25*time.Hour), 6, true)
	add(j5)

	// 6. Discarded.
	j6 := &mockJob{Job: store.Job{ID: "j-3be7f0", Title: "Spike: gRPC gateway for the dashboard", Site: "archwright", Outcome: "discarded", PurgedAt: ago(20 * time.Hour).Format(time.RFC3339), CreatedAt: ago(48 * time.Hour)}}
	add(j6)

	// 7. Paused mid-flight.
	j7 := &mockJob{Job: store.Job{ID: "j-6e4d19", Title: "Migrate logging to structured slog", Site: "archwright", PausedAt: ago(10 * time.Minute).Format(time.RFC3339), CreatedAt: ago(3 * time.Hour)}}
	j7.Plan = buildPlan(j7.Job.ID, "build", store.ApprovalNotRequired, ago(3*time.Hour))
	j7.Dispatches = []store.Dispatch{dispatch("d-701", j7.Job.ID, "explore", "survey", 0, store.DispatchReported, "r-701", ago(170*time.Minute))}
	j7.Runs = []store.RunRecord{run("r-701", j7.Job.ID, "d-701", "survey", "codex", 12000, 1500, 0, "done", ago(170*time.Minute), 2*time.Minute)}
	w.Events["r-701"] = runEvents("r-701", ago(170*time.Minute), 4, false)
	add(j7)

	// 8. Planning: no Plan yet.
	add(&mockJob{Job: store.Job{ID: "j-b82f77", Title: "Draft the onboarding guide", Site: "archwright", CreatedAt: ago(2 * time.Minute)}})

	// 9. Between steps; also queued work.
	j9 := &mockJob{Job: store.Job{ID: "j-44c0de", Title: "Rate-limit the public search endpoint", Site: "billing-api", CreatedAt: ago(90 * time.Minute)}}
	j9.Plan = buildPlan(j9.Job.ID, "build", store.ApprovalNotRequired, ago(88*time.Minute))
	j9.Dispatches = []store.Dispatch{dispatch("d-901", j9.Job.ID, "explore", "survey", 0, store.DispatchReported, "r-901", ago(80*time.Minute)),
		dispatch("d-902", j9.Job.ID, "implement", "masonry", 0, store.DispatchQueued, "", ago(1*time.Minute))}
	j9.Runs = []store.RunRecord{run("r-901", j9.Job.ID, "d-901", "survey", "claude", 25000, 2800, 0.1733, "done", ago(80*time.Minute), 3*time.Minute)}
	w.Events["r-901"] = runEvents("r-901", ago(80*time.Minute), 5, false)
	add(j9)

	// 10. investigate-and-fix, mid-flight: one trace done, the other running.
	j10 := &mockJob{Job: store.Job{ID: "j-a17c30", Title: "Intermittent 502s from the invoice export", Description: "The nightly export returns 502 for about 1 in 10 large accounts.", Site: "billing-api", CreatedAt: ago(25 * time.Minute)}}
	j10.Plan = investigatePlan(j10.Job.ID, ago(24*time.Minute))
	j10.Dispatches = []store.Dispatch{
		dispatch("d-1001", j10.Job.ID, "trace-code", "survey", 0, store.DispatchReported, "r-1001", ago(23*time.Minute)),
		dispatch("d-1002", j10.Job.ID, "trace-docs", "survey", 0, store.DispatchRunning, "r-1002", ago(4*time.Minute)),
	}
	j10.Runs = []store.RunRecord{
		run("r-1001", j10.Job.ID, "d-1001", "survey", "claude", 27400, 3300, 0.1822, "done", ago(23*time.Minute), 4*time.Minute),
		run("r-1002", j10.Job.ID, "d-1002", "survey", "claude", 9800, 1100, 0.0640, "running", ago(4*time.Minute), 0),
	}
	w.Events["r-1001"] = runEvents("r-1001", ago(23*time.Minute), 6, false)
	w.Events["r-1002"] = runEvents("r-1002", ago(4*time.Minute), 3, false)
	add(j10)

	// 11. investigate-and-fix, finished: every step done and accepted.
	j11 := &mockJob{Job: store.Job{ID: "j-c4d9e1", Title: "Session cookie not cleared on logout", Site: "archwright", Writ: "AW-81", Outcome: "accepted", CreatedAt: ago(30 * time.Hour)}}
	j11.Plan = investigatePlan(j11.Job.ID, ago(30*time.Hour))
	for i, st := range []struct{ piece, guild string }{{"trace-code", "survey"}, {"trace-docs", "survey"}, {"plan-fix", "chancery"}, {"fix", "masonry"}, {"review", "hallmark"}} {
		at := ago(time.Duration(29*60-i*40) * time.Minute)
		did, rid := fmt.Sprintf("d-110%d", i), fmt.Sprintf("r-110%d", i)
		j11.Dispatches = append(j11.Dispatches, dispatch(did, j11.Job.ID, st.piece, st.guild, 0, store.DispatchReported, rid, at))
		j11.Runs = append(j11.Runs, run(rid, j11.Job.ID, did, st.guild, "claude", 20000+i*7000, 2500+i*900, 0.15+float64(i)*0.11, "done", at, 5*time.Minute))
		w.Events[rid] = runEvents(rid, at, 5, true)
	}
	j11.Reviews = []store.Review{{ID: "rv-11", JobID: j11.Job.ID, Seq: 1, Branch: "aw/j-c4d9e1", SHA: "7a6b5c4", Status: store.ReviewReported, SpecBasis: store.SpecWrit, Overall: store.VerdictPass,
		Standards: &store.Axis{Verdict: store.VerdictPass, Findings: []store.Finding{}}, Spec: &store.Axis{Verdict: store.VerdictPass, Findings: []store.Finding{}}, CreatedAt: ago(25 * time.Hour)}}
	add(j11)

	// 12. A second ad hoc Plan, with different steps from j-2c9d04: a fan-out then a join.
	j12 := &mockJob{Job: store.Job{ID: "j-e05b62", Title: "Audit and bump vulnerable dependencies", Description: "npm audit reports 3 high severity advisories.", Site: "billing-api", CreatedAt: ago(70 * time.Minute)}}
	adhoc := steps("audit", "survey", "List advisories and affected packages",
		"bump-runtime", "masonry", "Bump runtime dependencies",
		"bump-dev", "masonry", "Bump dev dependencies",
		"changelog", "chancery", "Write the upgrade notes")
	adhoc[1].BlockedBy, adhoc[2].BlockedBy = []string{"audit"}, []string{"audit"}
	adhoc[3].BlockedBy = []string{"bump-runtime", "bump-dev"}
	j12.Plan = &store.Plan{JobID: j12.Job.ID, Steps: adhoc, Approval: store.ApprovalApproved, UpdatedAt: ago(66 * time.Minute)}
	j12.Dispatches = []store.Dispatch{
		dispatch("d-1201", j12.Job.ID, "audit", "survey", 0, store.DispatchReported, "r-1201", ago(64*time.Minute)),
		dispatch("d-1202", j12.Job.ID, "bump-runtime", "masonry", 0, store.DispatchRunning, "r-1202", ago(12*time.Minute)),
		dispatch("d-1203", j12.Job.ID, "bump-dev", "masonry", 0, store.DispatchBlocked, "r-1203", ago(20*time.Minute)),
	}
	j12.Runs = []store.RunRecord{
		run("r-1201", j12.Job.ID, "d-1201", "survey", "codex", 15200, 1900, 0, "done", ago(64*time.Minute), 3*time.Minute),
		run("r-1202", j12.Job.ID, "d-1202", "masonry", "claude", 48100, 6200, 0.3911, "running", ago(12*time.Minute), 0),
		run("r-1203", j12.Job.ID, "d-1203", "masonry", "claude", 22700, 2600, 0.1688, "done", ago(20*time.Minute), 6*time.Minute),
	}
	j12.Asks = []store.Ask{{ID: "ask-2", JobID: j12.Job.ID, Asker: "wright", Guild: "masonry", Piece: "bump-dev", DispatchID: "d-1203", Kind: store.AskDecision, Blocking: true,
		Why: "Upgrading the test runner is a major version bump.", Holder: store.HolderWarden, Status: store.AskOpen, Answers: []store.Answer{}, CreatedAt: ago(14 * time.Minute),
		Questions: []store.Question{{ID: "q1", Text: "Take the major bump of the test runner?", Options: []string{"Yes", "No, pin the current major"}, Default: "No, pin the current major"}}}}
	w.Events["r-1201"] = runEvents("r-1201", ago(64*time.Minute), 4, false)
	w.Events["r-1202"] = runEvents("r-1202", ago(12*time.Minute), 7, false)
	w.Events["r-1203"] = runEvents("r-1203", ago(20*time.Minute), 6, false)
	add(j12)

	w.Queue.Running = []works.QueueEntry{
		{DispatchID: "d-102", Job: j1.Job.ID, Piece: "implement", Guild: "masonry", Site: "billing-api"},
		{DispatchID: "d-1002", Job: j10.Job.ID, Piece: "trace-docs", Guild: "survey", Site: "billing-api"},
		{DispatchID: "d-1202", Job: j12.Job.ID, Piece: "bump-runtime", Guild: "masonry", Site: "billing-api"}}
	w.Queue.Queued = []works.QueueEntry{{DispatchID: "d-902", Job: j9.Job.ID, Piece: "implement", Guild: "masonry", Site: "billing-api", Position: 1, Waiting: "Warden cap (2 of 2 masonry Wrights running)"}}
	w.NeedsYou = append(w.NeedsYou,
		store.NeedsYouItem{ID: 2, Kind: store.NeedsPermissionWanted, JobID: j1.Job.ID, Site: "billing-api",
			Summary: "A runner denied `npm install left-pad`, which would have prompted.", Detail: json.RawMessage(`{"tool":"Bash","command":"npm install left-pad","runner":"codex"}`), CreatedAt: ago(4 * time.Minute)},
		store.NeedsYouItem{ID: 3, Kind: store.NeedsScopeOverlap, JobID: j9.Job.ID, Site: "billing-api",
			Summary: "Piece implement overlaps files with j-7f3a21 (src/webhooks/sender.ts).", Detail: json.RawMessage(`{}`), CreatedAt: ago(1 * time.Minute)},
		store.NeedsYouItem{ID: 4, Kind: store.NeedsAsk, JobID: j3.Job.ID, Site: "archwright", Summary: "Ask ask-1 reached the Archwright.", Detail: json.RawMessage(`{}`), CreatedAt: ago(25 * time.Minute)},
	)
}

func mockCatalogue() works.Catalogue {
	return works.Catalogue{
		Guilds: []works.CatalogueGuild{
			{Name: "chancery", Intake: "A topic to draft a ticket for", Handback: "A drafted ticket", Access: defs.ReadOnly, Runners: []string{"claude"}, Source: "shipped"},
			{Name: "hallmark", Intake: "A branch to review", Handback: "Verdicts on standards and spec", Access: defs.ReadOnly, Runners: []string{"claude", "codex"}, Source: "shipped"},
			{Name: "masonry", Intake: "A scoped implementation brief", Handback: "A branch with passing gates", Access: defs.ReadWrite, Runners: []string{"claude", "codex"}, Source: "shipped"},
			{Name: "survey", Intake: "A question about the code or docs", Handback: "A written finding", Access: defs.ReadOnly, Runners: []string{}, Source: "shipped"},
			{Name: "local-docs", Intake: "Docs to update", Handback: "A docs branch", Access: defs.ReadWrite, Runners: []string{"claude"}, Source: "~/.archwright/guilds/local-docs"},
		},
		Jigs: []defs.Jig{{Name: "build", Description: "Survey, implement and review a change", Source: "shipped",
			Parameters: []defs.Param{{Name: "ticket", Description: "The ticket to build", Required: true}, {Name: "base", Default: "main"}},
			Steps: []defs.JigStep{
				{ID: "explore", Guild: "survey", Task: "Trace the current code path"},
				{ID: "implement", Guild: "masonry", Task: "Implement the change", BlockedBy: []string{"explore"}},
				{ID: "review", Guild: "hallmark", Task: "Review against the spec", BlockedBy: []string{"implement"}, Checkpoint: true},
			}},
			{Name: "investigate-and-fix", Description: "Trace a bug from two angles in parallel, fix it, then review", Source: "~/.archwright/jigs/investigate-and-fix.yaml",
				Parameters: []defs.Param{{Name: "report", Description: "The bug report or failing test", Required: true}},
				Steps: []defs.JigStep{
					{ID: "trace-code", Guild: "survey", Task: "Trace the failing code path"},
					{ID: "trace-docs", Guild: "survey", Task: "Check docs and ADRs for intended behaviour"},
					{ID: "plan-fix", Guild: "chancery", Task: "Draft the fix as a ticket", BlockedBy: []string{"trace-code", "trace-docs"}, Checkpoint: true},
					{ID: "fix", Guild: "masonry", Task: "Implement the fix with a regression test", BlockedBy: []string{"plan-fix"}},
					{ID: "review", Guild: "hallmark", Task: "Review against the ticket", BlockedBy: []string{"fix"}},
				}}},
		Errors: []defs.LoadError{{Kind: "jig", Name: "broken-jig", Source: "~/.archwright/jigs/broken-jig.yaml", Reason: "step \"b\" is blocked by unknown step \"z\""}},
	}
}
