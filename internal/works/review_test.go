package works

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

type reviewRig struct {
	*gateRig
	rv *FakeReviewer
}

func newReviewRig(t *testing.T, script ...*ReviewReport) *reviewRig {
	return newReviewRigGates(t, store.Gates{Test: "true"}, script...)
}

func newReviewRigGates(t *testing.T, gates store.Gates, script ...*ReviewReport) *reviewRig {
	g := newGateRig(t, gates)
	rv := NewFakeReviewer(script...)
	g.r.Configure(func(name string) (Guild, error) {
		switch name {
		case "masonry", ReviewGuild:
			return Guild{Name: name, Standards: name + " standards."}, nil
		}
		return Guild{}, errInvalidInput
	}, g.runner)
	g.r.ConfigureReview(rv)
	return &reviewRig{gateRig: g, rv: rv}
}

func failReport(std, spec string) *ReviewReport {
	ax := func(file, reason string) store.Axis {
		if file == "" {
			return store.Axis{Verdict: store.VerdictPass}
		}
		return store.Axis{Verdict: store.VerdictFail, Findings: []store.Finding{{File: file, Line: 7, Severity: store.SeverityMajor, Reason: reason}}}
	}
	return &ReviewReport{Standards: ax(std, "breaks a convention"), Spec: ax(spec, "not what was asked")}
}

func (g *reviewRig) writ() {
	g.ok(g.arch, "link_writ", `{"job_id":"`+g.job.ID+`","writ":"Ticket 8: add notes"}`, nil)
}

func (g *reviewRig) jobArgs() string { return `{"job_id":"` + g.job.ID + `"}` }

// review requests a review and waits for it to finish.
func (g *reviewRig) review() ReviewState {
	g.t.Helper()
	g.ok(g.arch, "request_review", g.jobArgs(), nil)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var s ReviewState
		g.ok(g.arch, "show_review", g.jobArgs(), &s)
		if n := len(s.Reviews); n > 0 && s.Reviews[n-1].Status != store.ReviewRunning {
			return s
		}
	}
	g.t.Fatal("review never finished")
	return ReviewState{}
}

func (g *reviewRig) job2() store.Job {
	j, _ := g.st.GetJob(g.job.ID)
	return j
}

func TestPassOnBothAxesAllowsGoAhead(t *testing.T) {
	g := newReviewRig(t)
	g.writ()
	g.commit("notes.md", "x")
	g.handBack("p")
	s := g.review()
	last := s.Reviews[0]
	if last.Overall != store.VerdictPass || last.SpecBasis != store.SpecWrit || s.Next != "pr_go_ahead" {
		t.Fatalf("%+v next=%s", last, s.Next)
	}
	g.ok(g.arch, "pr_go_ahead", g.jobArgs(), nil)
	if g.job2().PRGoAhead == "" {
		t.Fatal("no go-ahead recorded")
	}
	var st struct{ Granted bool }
	g.ok(g.warden, "pr_go_ahead_status", `{}`, &st)
	if !st.Granted {
		t.Fatal("engineering Warden cannot see the go-ahead")
	}
}

func TestGoAheadBlockedWithoutOverallPass(t *testing.T) {
	g := newReviewRig(t, failReport("a.go", ""), nil)
	g.writ()
	g.fails(g.arch, "pr_go_ahead", g.jobArgs(), spool.CodeConflict) // never reviewed
	g.commit("a.go", "x")
	g.handBack("p")
	g.review() // fail
	g.fails(g.arch, "pr_go_ahead", g.jobArgs(), spool.CodeConflict)
	var st struct{ Granted bool }
	g.ok(g.warden, "pr_go_ahead_status", `{}`, &st)
	if st.Granted || g.job2().PRGoAhead != "" {
		t.Fatal("go-ahead recorded after a fail")
	}
}

func TestSpecFallsBackAndWithNothingIsNotEvaluated(t *testing.T) {
	g := newReviewRig(t)
	g.commit("a.go", "x")
	g.handBack("p")
	// no writ, no step brief, no request: the axis is not evaluated whatever the Warden says
	s := g.review()
	r := s.Reviews[0]
	if r.SpecBasis != store.SpecNone || r.Spec.Verdict != store.VerdictNotEvaluated || r.Standards.Verdict != store.VerdictPass || r.Overall != store.VerdictNotEvaluated {
		t.Fatalf("%+v", r)
	}
	g.fails(g.arch, "pr_go_ahead", g.jobArgs(), spool.CodeConflict)
	if items, _ := g.st.NeedsYou(); len(items) != 1 || items[0].Kind != store.NeedsReviewNotEvaluated {
		t.Fatalf("%+v", items)
	}
	if strings.Contains(r.Brief, "Plan step briefs") {
		t.Fatal("the brief invents a spec")
	}

	// the Plan step's brief and the original request stand in for a missing writ
	g.ok(g.arch, "set_plan", `{"job_id":"`+g.job.ID+`","steps":[{"id":"a","guild":"masonry","title":"notes","brief":"Add a notes file"},{"id":"b","guild":"hallmark","title":"review","brief":"ignored","blocked_by":["a"]}]}`, nil)
	r = g.review().Reviews[1]
	if r.SpecBasis != store.SpecPlanAndRequest || r.Overall != store.VerdictPass || !strings.Contains(r.Brief, "Add a notes file") || strings.Contains(r.Brief, "ignored") {
		t.Fatalf("%+v", r)
	}

	// a linked writ beats both
	g.writ()
	r = g.review().Reviews[2]
	if r.SpecBasis != store.SpecWrit || !strings.Contains(r.Brief, "Ticket 8") || strings.Contains(r.Brief, "Add a notes file") {
		t.Fatalf("%+v", r)
	}
}

func TestRequestAloneIsASpecBasis(t *testing.T) {
	r, st := newRuns(t)
	job, _ := st.CreateJob("Title only", "")
	if basis, _, err := specMaterial(st, job); err != nil || basis != store.SpecNone {
		t.Fatalf("a title is a label, not a spec: %q %v", basis, err)
	}
	job, _ = st.CreateJob("Notes", "Please add notes")
	basis, material, err := specMaterial(st, job)
	if err != nil || basis != store.SpecPlanAndRequest || !strings.Contains(material, "Please add notes") {
		t.Fatalf("%q %q %v", basis, material, err)
	}
	_ = r
}

func TestAxesAreIndependentAndWardenNotEvaluatedCannotPass(t *testing.T) {
	g := newReviewRig(t,
		failReport("", "a.go"),
		&ReviewReport{Standards: store.Axis{Verdict: store.VerdictPass}, Spec: store.Axis{Verdict: store.VerdictNotEvaluated, Reason: "writ is unreadable"}},
	)
	g.writ()
	g.commit("a.go", "x")
	g.handBack("p")
	r := g.review().Reviews[0]
	if r.Standards.Verdict != store.VerdictPass || r.Spec.Verdict != store.VerdictFail || r.Overall != store.VerdictFail {
		t.Fatalf("%+v", r)
	}
	g.ok(g.arch, "issue_review_rework", g.jobArgs(), nil)
	g.commit("b.go", "y")
	g.ok(g.warden, "handback", handbackJSON("done", `"again"`, ""), nil)
	r = g.review().Reviews[1]
	if r.Overall != store.VerdictNotEvaluated || r.Spec.Reason == "" {
		t.Fatalf("%+v", r)
	}
	g.fails(g.arch, "pr_go_ahead", g.jobArgs(), spool.CodeConflict)
	if g.job2().ParkedFor != "" {
		t.Fatal("not evaluated is not a fail, and must not park")
	}
}

func TestOneAutomaticReworkThenPark(t *testing.T) {
	g := newReviewRig(t, failReport("a.go", "a.go"), failReport("b.go", ""))
	g.writ()
	g.commit("a.go", "x")
	g.handBack("p")
	s := g.review()
	if s.Next != "issue_review_rework" || s.Reviews[0].Overall != store.VerdictFail {
		t.Fatalf("%+v", s)
	}
	// re-reviewing before the rework is issued, or without a new handback, is refused
	var rw ReworkInfo
	g.ok(g.arch, "issue_review_rework", g.jobArgs(), &rw)
	if len(rw.Constraints) != 2 || !strings.Contains(rw.Constraints[0], "a.go:7") || !strings.Contains(rw.Constraints[0], "breaks a convention") || !strings.Contains(rw.Constraints[1], "not what was asked") {
		t.Fatalf("%+v", rw)
	}
	g.fails(g.arch, "issue_review_rework", g.jobArgs(), spool.CodeConflict) // only once
	g.fails(g.arch, "request_review", g.jobArgs(), spool.CodeConflict)      // Masonry has not handed back
	var st ReviewState
	g.ok(g.arch, "show_review", g.jobArgs(), &st)
	if st.Next != "await_rework_handback" || len(st.Reviews[0].ReworkConstraints) != 2 {
		t.Fatalf("%+v", st)
	}
	g.commit("b.go", "y")
	g.ok(g.warden, "handback", handbackJSON("done", `"again"`, ""), nil)
	s = g.review()
	if s.Reviews[1].Overall != store.VerdictFail || s.Next != "parked" {
		t.Fatalf("%+v", s)
	}
	j := g.job2()
	if j.ParkedFor == "" {
		t.Fatal("the Job is not parked")
	}
	if items, _ := g.st.NeedsYou(); len(items) != 1 || items[0].Kind != store.NeedsReviewParked || items[0].JobID != g.job.ID {
		t.Fatalf("%+v", items)
	}
	g.fails(g.arch, "issue_review_rework", g.jobArgs(), spool.CodeConflict)
	g.fails(g.arch, "request_review", g.jobArgs(), spool.CodeConflict)
	g.fails(g.arch, "pr_go_ahead", g.jobArgs(), spool.CodeConflict)
	if n := len(g.rv.Requests()); n != 2 {
		t.Fatalf("%d review runs", n)
	}
}

func TestReworkThenPassAllowsGoAhead(t *testing.T) {
	g := newReviewRig(t, failReport("a.go", ""))
	g.writ()
	g.commit("a.go", "x")
	g.handBack("p")
	g.review()
	g.ok(g.arch, "issue_review_rework", g.jobArgs(), nil)
	g.commit("a.go", "fixed")
	g.ok(g.warden, "handback", handbackJSON("done", `"again"`, ""), nil)
	if s := g.review(); s.Reviews[1].Overall != store.VerdictPass || s.Next != "pr_go_ahead" {
		t.Fatalf("%+v", s)
	}
	g.ok(g.arch, "pr_go_ahead", g.jobArgs(), nil)
}

func TestPassIsStaleOnceTheBranchMoves(t *testing.T) {
	g := newReviewRig(t)
	g.writ()
	g.commit("a.go", "x")
	g.handBack("p")
	g.review()
	g.commit("a.go", "changed after review")
	g.fails(g.arch, "pr_go_ahead", g.jobArgs(), spool.CodeConflict)
	var st ReviewState
	g.ok(g.arch, "show_review", g.jobArgs(), &st)
	if st.Next != "request_review" {
		t.Fatal(st.Next)
	}
}

func TestReviewNeedsADoneHandbackAndPassingGates(t *testing.T) {
	g := newReviewRigGates(t, store.Gates{Test: "false"})
	g.writ()
	g.fails(g.arch, "request_review", g.jobArgs(), spool.CodeConflict) // nothing handed back
	g.handBack("p")
	g.fails(g.arch, "request_review", g.jobArgs(), spool.CodeConflict) // gates failed
}

func TestReviewWardenIsFreshAndReadOnly(t *testing.T) {
	g := newReviewRig(t)
	g.writ()
	g.commit("a.go", "x")
	g.handBack("p")
	g.review()
	g.ok(g.warden, "handback", handbackJSON("done", `"SECRET-MASONRY-HANDBACK"`, ""), nil)
	g.review()
	reqs := g.rv.Requests()
	if len(reqs) != 2 || reqs[0].Env.Run == reqs[1].Env.Run || reqs[0].Env.Run == g.warden.Run {
		t.Fatalf("review runs are not fresh: %+v", reqs)
	}
	// nothing of Masonry's reaches it: not its brief, its standards, its results or handback
	for _, req := range reqs {
		for _, leak := range []string{"Add login", "masonry standards", "fake://", "SECRET-MASONRY", "fake runner"} {
			if strings.Contains(req.Brief, leak) {
				t.Errorf("review brief leaks Masonry's %q", leak)
			}
		}
		if !strings.Contains(req.Brief, "hallmark standards.") || !strings.Contains(req.Brief, "do not re-run") && !strings.Contains(req.Brief, "Do not re-run") {
			t.Errorf("brief: %s", req.Brief)
		}
	}
	// a detached worktree of the integration tip, not the integration worktree itself
	_, tip, _ := g.sm.IntegrationTip(g.job.ID)
	wts, _ := g.st.Worktrees(g.job.ID)
	var ro *store.Worktree
	for i := range wts {
		if wts[i].Path == reqs[0].Worktree {
			ro = &wts[i]
		}
	}
	if ro == nil || ro.Kind != store.KindReadOnly || ro.Branch != "" || reqs[0].Worktree == g.worktree.Path {
		t.Fatalf("%+v", ro)
	}
	if rev, _ := g.st.GetReview(reqs[0].ReviewID); rev.SHA == "" || rev.SHA != ro.BaseSHA || rev.SHA != git(t, reqs[0].Worktree, "rev-parse", "HEAD") || rev.SHA != tip && len(reqs) == 1 {
		t.Fatalf("%+v", rev)
	}
	// the review Warden can only report: it cannot dispatch, hand back, or grant anything
	env, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: ReviewGuild, Review: "rev-x"})
	if err != nil {
		t.Fatal(err)
	}
	g.fails(env, "dispatch_wright", `{"piece":"z","brief":`+goodBrief+`}`, spool.CodeInvalidInput)
	g.fails(env, "handback", handbackJSON("done", `"x"`, ""), spool.CodeInvalidInput)
	g.fails(env, "pr_go_ahead", g.jobArgs(), spool.CodeForbidden)
	g.fails(env, "request_review", g.jobArgs(), spool.CodeForbidden)
	// and an Masonry Warden cannot report a verdict
	g.fails(g.warden, "report_review", `{"standards":{"verdict":"pass"},"spec":{"verdict":"pass"}}`, spool.CodeInvalidInput)
}

func TestReportShapeIsEnforced(t *testing.T) {
	g := newReviewRig(t)
	g.handBack("p")
	hb, _ := g.st.LatestHandback(g.job.ID)
	rev, err := g.st.NewReview(g.job.ID, hb.ID, "b", "sha", store.SpecWrit, func(store.Review) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	env, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: ReviewGuild, Review: rev.ID})
	if err != nil {
		t.Fatal(err)
	}
	f := func(sev string, line int) string {
		return `{"file":"a.go","line":` + strconv.Itoa(line) + `,"severity":"` + sev + `","reason":"r"}`
	}
	pass := `{"verdict":"pass"}`
	for name, in := range map[string]string{
		"unknown verdict":      `{"standards":{"verdict":"great"},"spec":` + pass + `}`,
		"missing axis":         `{"standards":` + pass + `}`,
		"fail without finding": `{"standards":{"verdict":"fail"},"spec":` + pass + `}`,
		"finding no line":      `{"standards":{"verdict":"fail","findings":[` + f("major", 0) + `]},"spec":` + pass + `}`,
		"bad severity":         `{"standards":{"verdict":"fail","findings":[` + f("huge", 3) + `]},"spec":` + pass + `}`,
		"pass with major":      `{"standards":{"verdict":"pass","findings":[` + f("major", 3) + `]},"spec":` + pass + `}`,
		"not evaluated no why": `{"standards":{"verdict":"not_evaluated"},"spec":` + pass + `}`,
		"unknown field":        `{"standards":` + pass + `,"spec":` + pass + `,"overall":"pass"}`,
	} {
		if resp := g.call(env, "report_review", in); resp.OK || resp.Error.Code != spool.CodeInvalidInput {
			t.Errorf("%s: %+v", name, resp)
		}
	}
	// a pass may carry minor notes
	g.ok(env, "report_review", `{"standards":{"verdict":"pass","findings":[`+f("minor", 3)+`]},"spec":`+pass+`}`, nil)
	g.fails(env, "report_review", `{"standards":`+pass+`,"spec":`+pass+`}`, spool.CodeConflict) // once
}

func TestReviewThatNeverReportsErrorsAndMayBeRetried(t *testing.T) {
	g := newReviewRig(t, nil)
	g.writ()
	g.commit("a.go", "x")
	g.handBack("p")
	s := g.review()
	if s.Reviews[0].Status != store.ReviewErrored || s.Reviews[0].Reason == "" || s.Next != "request_review" {
		t.Fatalf("%+v", s)
	}
	if s := g.review(); s.Reviews[1].Overall != store.VerdictPass {
		t.Fatalf("%+v", s)
	}
	g.fails(g.arch, "request_review", `{"job_id":"nope"}`, spool.CodeNotFound)
}

func TestWritAndReviewsOverAPI(t *testing.T) {
	var n atomic.Int32
	c, err := Connect(context.Background(), dirs(t), inProcess(t, &n))
	if err != nil {
		t.Fatal(err)
	}
	var j store.Job
	if err := c.Do("POST", "/jobs", map[string]string{"title": "T"}, &j); err != nil {
		t.Fatal(err)
	}
	if err := c.Do("PUT", "/jobs/"+j.ID+"/writ", map[string]string{"writ": " "}, nil); err == nil {
		t.Fatal("blank writ accepted")
	}
	if err := c.Do("PUT", "/jobs/"+j.ID+"/writ", map[string]string{"writ": "Ticket 8"}, &j); err != nil || j.Writ != "Ticket 8" {
		t.Fatalf("%+v %v", j, err)
	}
	var s ReviewState
	if err := c.Do("GET", "/jobs/"+j.ID+"/reviews", nil, &s); err != nil || len(s.Reviews) != 0 || s.Next != "request_review" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestRunningReviewIsErroredWhenTheWorksRestarts(t *testing.T) {
	g := newReviewRig(t)
	g.handBack("p")
	hb, _ := g.st.LatestHandback(g.job.ID)
	rev, err := g.st.NewReview(g.job.ID, hb.ID, "b", "sha", store.SpecWrit, func(store.Review) string { return "brief" })
	if err != nil || rev.Brief != "brief" {
		t.Fatalf("%+v %v", rev, err)
	}
	if err := g.st.InterruptReviews(); err != nil {
		t.Fatal(err)
	}
	if got, _ := g.st.GetReview(rev.ID); got.Status != store.ReviewErrored || got.Reason == "" {
		t.Fatalf("%+v", got)
	}
}

func TestGoAheadDoesNotOutliveTheReviewedCode(t *testing.T) {
	g := newReviewRig(t)
	g.writ()
	g.commit("notes.md", "x")
	g.handBack("p")
	g.review()
	g.ok(g.arch, "pr_go_ahead", g.jobArgs(), nil)

	g.commit("more.md", "y")
	g.ok(g.warden, "handback", handbackJSON("done", `"again"`, ""), nil)
	var st struct{ Granted bool }
	g.ok(g.warden, "pr_go_ahead_status", `{}`, &st)
	var rs ReviewState
	g.ok(g.arch, "show_review", g.jobArgs(), &rs)
	if st.Granted || rs.Next != "request_review" {
		t.Fatalf("stale go-ahead honoured: granted=%v next=%s", st.Granted, rs.Next)
	}
	g.fails(g.arch, "pr_go_ahead", g.jobArgs(), spool.CodeConflict)

	rs = g.review()
	if rs.Next != "pr_go_ahead" || g.job2().PRGoAhead != "" {
		t.Fatalf("a new review inherited the old go-ahead: next=%s", rs.Next)
	}
	g.ok(g.warden, "pr_go_ahead_status", `{}`, &st)
	if st.Granted {
		t.Fatal("granted without a go-ahead for the new review")
	}
}

func TestRequestReviewExplainsMissingRunner(t *testing.T) {
	g := newGateRig(t, store.Gates{Test: "true"})
	g.commit("a.go", "x")
	g.handBack("p")
	resp := g.call(g.arch, "request_review", `{"job_id":"`+g.job.ID+`"}`)
	if resp.OK || resp.Error.Code != spool.CodeInvalidInput || !strings.Contains(resp.Error.Message, "runner adapter") {
		t.Fatalf("%+v", resp)
	}
}
