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
	Raw       map[string]json.RawMessage // raw vendor event by "run/seq"; absent for the generic mock events
	Catalogue works.Catalogue
	Queue     works.QueueStatus
	seq       int // numbers what the simulation creates
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
	w := &mockWorld{Events: map[string][]store.RunEvent{}, Raw: map[string]json.RawMessage{}}
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

// ---- scripted run transcripts -------------------------------------------------------------------------
// A turn is one run event in the shape the real runner reports it: its kind and one-line summary, and the raw
// vendor event behind it (assistant text, a tool call, the tool's output, the final result).
type turn struct {
	kind, summary string
	raw           any
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			s = s[:i]
			break
		}
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func tNote(kind, summary string) turn { return turn{kind: kind, summary: summary} }
func tMsg(text string) turn {
	return turn{"message", firstLine(text), map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}}}
}
func tUse(id, cmd string) turn {
	return turn{"tool_use", "Bash: " + firstLine(cmd), map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]string{"command": cmd}}}}}}
}
func tRes(id, out string, isErr bool) turn {
	sum := firstLine(out)
	if isErr {
		sum = "error: " + sum
	}
	return turn{"tool_result", sum, map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "content": out, "is_error": isErr}}}}}
}
func tFinal(text string) turn {
	return turn{"result", firstLine(text), map[string]any{"type": "result", "result": text}}
}

// script records a run's turns, one every gap from start; n < len(turns) cuts it short, like a run still going.
func (w *mockWorld) script(runID string, start time.Time, gap time.Duration, turns []turn, n int) {
	if n <= 0 || n > len(turns) {
		n = len(turns)
	}
	var evs []store.RunEvent
	for i, t := range turns[:n] {
		evs = append(evs, store.RunEvent{RunID: runID, Seq: i + 1, Kind: t.kind, Summary: t.summary, HasRaw: t.raw != nil, At: start.Add(time.Duration(i) * gap)})
		if t.raw != nil {
			b, _ := json.Marshal(t.raw)
			w.Raw[fmt.Sprintf("%s/%d", runID, i+1)] = b
		}
	}
	w.Events[runID] = evs
}

// wardenRun scripts a Warden carrying out the Plan step for piece: dispatch a Wright, wait, accept, hand back.
func (w *mockWorld) wardenRun(runID, guild, piece string, start time.Time, gap time.Duration, n int) {
	t := func(i int) string { return fmt.Sprintf("%s-t%d", runID, i) }
	w.script(runID, start, gap, []turn{
		tNote("posture", "claude 2.1.285 (Claude Code): default mode, inherited sandbox"),
		tNote("started", "session started"),
		tMsg("I'll read the step brief, then dispatch one " + guild + " Wright for " + piece + " and check its work before handing back."),
		tUse(t(1), `echo '{"piece":"`+piece+`","brief":{"objective":"`+piece+`","scope":{"in":["internal/"]},"constraints":[],"inputs":["the step brief"],"definition_of_done":["the step is done"],"return_shape":"a pointer to the result","limits":{"timeout_minutes":30}}}' | archwright tool dispatch_wright`),
		tRes(t(1), "{\n  \"ok\": true,\n  \"dispatch\": \"dsp-"+runID+"\",\n  \"status\": \"queued\"\n}", false),
		tUse(t(2), `echo '{"piece":"`+piece+`","wait_seconds":45}' | archwright tool inspect_wright`),
		tRes(t(2), "{\n  \"piece\": \""+piece+"\",\n  \"dispatches\": [{\"round\": 0, \"status\": \"reported\"}]\n}", false),
		tUse(t(3), `echo '{"piece":"`+piece+`"}' | archwright tool accept_piece`),
		tRes(t(3), "{\n  \"ok\": true,\n  \"merged\": \"aw/integration\"\n}", false),
		tUse(t(4), `echo '{"status":"done","results":["aw/integration"],"checked":["inspected `+piece+`"],"rejected":[],"uncertainty":"","flags":[]}' | archwright tool handback`),
		tRes(t(4), "{\n  \"handback_id\": 12,\n  \"status\": \"done\",\n  \"flags_recorded\": 0\n}", false),
		tMsg("Accepted " + piece + " and handed back done."),
		tFinal("Accepted " + piece + "; the handback is filed."),
	}, n)
}

// wrightRun scripts a Wright doing the piece: read the code, change it, run the tests.
func (w *mockWorld) wrightRun(runID, guild, piece string, start time.Time, gap time.Duration, n int) {
	t := func(i int) string { return fmt.Sprintf("%s-t%d", runID, i) }
	posture := "claude 2.1.285 (Claude Code): default mode, inherited sandbox"
	if guild == "survey" || guild == "hallmark" {
		posture = "claude 2.1.285 (Claude Code): plan mode, inherited sandbox"
	}
	w.script(runID, start, gap, []turn{
		tNote("posture", posture),
		tNote("started", "session started"),
		tMsg("Reading the code around " + piece + " first."),
		tUse(t(1), "git log --oneline -5"),
		tRes(t(1), "3490797 fix(works): wake idle session\n98d848d fix(jigs): add the open-pr step\n3cf33f0 fix(works): nudge with an NDJSON user message", false),
		tUse(t(2), "grep -rn \"Page(\" internal/inventory"),
		tRes(t(2), "internal/inventory/inventory.go:83:func (s *Store) Page(page, size int) []Item {", false),
		tMsg("The end bound is clamped to len-1, but the slice upper bound is exclusive, so the last Item is dropped. I'll fix it and add a table-driven test."),
		tUse(t(3), "go test ./internal/inventory/..."),
		tRes(t(3), "--- FAIL: TestPageRemainder (0.00s)\n    inventory_test.go:41: page 3 of 5 items size 2: got [], want [e]\nFAIL", true),
		tUse(t(4), "go test ./internal/inventory/..."),
		tRes(t(4), "ok  \tgithub.com/example/pantry/internal/inventory\t0.004s", false),
		tFinal(piece + " done: the fix is in and the new test passes."),
	}, n)
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
	j1.Runs = append(j1.Runs,
		run("r-w101", j1.Job.ID, "", "survey", "claude", 18200, 2300, 0.1204, "done", ago(39*time.Minute), 6*time.Minute),
		run("r-w102", j1.Job.ID, "", "masonry", "claude", 14100, 1700, 0.0982, "running", ago(7*time.Minute), 0))
	w.wardenRun("r-w101", "survey", "explore", ago(39*time.Minute), 25*time.Second, 0)
	w.wrightRun("r-101", "survey", "explore", ago(38*time.Minute), 18*time.Second, 0)
	w.wardenRun("r-w102", "masonry", "implement", ago(7*time.Minute), 20*time.Second, 7)
	w.wrightRun("r-102", "masonry", "implement", ago(6*time.Minute), 18*time.Second, 9)
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
	j4.Runs = append(j4.Runs, run("r-w401", j4.Job.ID, "", "masonry", "claude", 31000, 4200, 0.2210, "done", ago(4*time.Hour+time.Minute), 72*time.Minute))
	w.wardenRun("r-w401", "masonry", "implement", ago(4*time.Hour+time.Minute), 4*time.Minute, 0)
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
		wid := fmt.Sprintf("r-w110%d", i)
		j11.Runs = append(j11.Runs, run(wid, j11.Job.ID, "", st.guild, "claude", 9000+i*1500, 1200+i*300, 0.06+float64(i)*0.03, "done", at.Add(-time.Minute), 7*time.Minute))
		w.wardenRun(wid, st.guild, st.piece, at.Add(-time.Minute), 30*time.Second, 0)
		w.wrightRun(rid, st.guild, st.piece, at, 25*time.Second, 0)
	}
	j11.Reviews = []store.Review{{ID: "rv-11", JobID: j11.Job.ID, Seq: 1, Branch: "aw/j-c4d9e1", SHA: "7a6b5c4", Status: store.ReviewReported, SpecBasis: store.SpecWrit, Overall: store.VerdictPass,
		Standards: &store.Axis{Verdict: store.VerdictPass, Findings: []store.Finding{}}, Spec: &store.Axis{Verdict: store.VerdictPass, Findings: []store.Finding{}}, CreatedAt: ago(25 * time.Hour)}}
	add(j11)

	// 13. A long-running Job: eight steps over most of a working day, with rework rounds and a long wait for the developer.
	j13 := &mockJob{Job: store.Job{ID: "j-9a77c3", Title: "Migrate the billing ledger to double-entry bookkeeping", Description: "Every ledger write becomes a balanced journal entry; history is backfilled.", Site: "billing-api", Writ: "BILL-340", CreatedAt: ago(11 * time.Hour)}}
	p13 := steps("explore", "survey", "Map every ledger write",
		"design", "chancery", "Draft the double-entry design",
		"schema", "masonry", "Add the journal tables and migration",
		"backfill", "masonry", "Backfill history into journal entries",
		"api", "masonry", "Switch the API to post journal entries",
		"ui", "masonry", "Update the statements view",
		"review", "hallmark", "Review against the design",
		"open-pr", "masonry", "Open the pull request")
	for i := 1; i < len(p13); i++ {
		p13[i].BlockedBy = []string{p13[i-1].ID}
	}
	j13.Plan = &store.Plan{JobID: j13.Job.ID, Steps: p13, Jig: "build", Approval: store.ApprovalNotRequired, UpdatedAt: ago(11 * time.Hour)}
	type leg struct {
		step, guild string
		startMin    int   // minutes after the Job started
		rounds      []int // Wright minutes per round; every round but the last was sent back
	}
	t13 := ago(11 * time.Hour)
	for li, l := range []leg{{"explore", "survey", 0, []int{14}}, {"design", "chancery", 20, []int{26, 18}}, {"schema", "masonry", 285, []int{31}},
		{"backfill", "masonry", 330, []int{48, 36, 22}}, {"api", "masonry", 470, []int{41, 27}}, {"ui", "masonry", 545, []int{35}},
		{"review", "hallmark", 595, []int{19, 12}}, {"open-pr", "masonry", 652, []int{6}}} {
		final := li == 7 // the last step is still running
		wstart := t13.Add(time.Duration(l.startMin) * time.Minute)
		cur := wstart.Add(time.Minute)
		for ri, mins := range l.rounds {
			did, rid := fmt.Sprintf("d-13%02d%d", li, ri), fmt.Sprintf("r-13%02d%d", li, ri)
			dst, rst := store.DispatchRejected, "done"
			if ri == len(l.rounds)-1 {
				dst = store.DispatchReported
			}
			dur := time.Duration(mins) * time.Minute
			if final {
				dst, rst, dur = store.DispatchRunning, "running", 0
			}
			j13.Dispatches = append(j13.Dispatches, dispatch(did, j13.Job.ID, l.step, l.guild, ri, dst, rid, cur))
			j13.Runs = append(j13.Runs, run(rid, j13.Job.ID, did, l.guild, "claude", 30000+mins*900, 3000+mins*120, 0.05*float64(mins), rst, cur, dur))
			w.wrightRun(rid, l.guild, l.step, cur, 20*time.Second, map[bool]int{true: 8, false: 0}[final])
			cur = cur.Add(dur + 3*time.Minute)
		}
		wid, wst, wdur := fmt.Sprintf("r-w13%02d", li), "done", cur.Sub(wstart)
		if final {
			wst, wdur = "running", 0
		}
		j13.Runs = append(j13.Runs, run(wid, j13.Job.ID, "", l.guild, "claude", 12000, 1800, 0.09, wst, wstart, wdur))
		w.wardenRun(wid, l.guild, l.step, wstart, 30*time.Second, map[bool]int{true: 7, false: 0}[final])
	}
	add(j13)

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
	j12.Runs = append(j12.Runs,
		run("r-w1201", j12.Job.ID, "", "survey", "codex", 8000, 900, 0, "done", ago(65*time.Minute), 5*time.Minute),
		run("r-w1203", j12.Job.ID, "", "masonry", "claude", 12000, 1500, 0.0880, "done", ago(21*time.Minute), 7*time.Minute),
		run("r-w1202", j12.Job.ID, "", "masonry", "claude", 10400, 1300, 0.0731, "running", ago(13*time.Minute), 0))
	w.wardenRun("r-w1201", "survey", "audit", ago(65*time.Minute), 25*time.Second, 0)
	w.wrightRun("r-1201", "survey", "audit", ago(64*time.Minute), 18*time.Second, 0)
	w.wardenRun("r-w1203", "masonry", "bump-dev", ago(21*time.Minute), 25*time.Second, 0)
	w.wrightRun("r-1203", "masonry", "bump-dev", ago(20*time.Minute), 20*time.Second, 0)
	w.wardenRun("r-w1202", "masonry", "bump-runtime", ago(13*time.Minute), 20*time.Second, 6)
	w.wrightRun("r-1202", "masonry", "bump-runtime", ago(12*time.Minute), 20*time.Second, 8)
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
