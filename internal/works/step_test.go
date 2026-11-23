package works

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

type stepRig struct {
	t      *testing.T
	r      *Runs
	st     *store.Store
	warden *FakeWarden
	arch   RunEnv
	job    store.Job
	n      int
}

func masonryGuilds(name string) (Guild, error) {
	if name != "masonry" {
		return Guild{}, errInvalidInput
	}
	return Guild{Name: name, Standards: "Write tests first."}, nil
}

// newStepRig is a Job with a two-step Plan (a, then b blocked by a), an
// approved or pending ad hoc Plan, a fake Wright runner and a fake Warden.
func newStepRig(t *testing.T, approved bool) *stepRig {
	r, st := newRuns(t)
	r.Configure(masonryGuilds, NewFakeRunner(nil))
	w := NewFakeWarden()
	r.ConfigureWarden(w)
	job, err := st.CreateJob("Add login", "with a form")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPlan(job.ID, []store.Step{
		{ID: "a", Guild: "masonry", Title: "Build the form", Brief: "Use the design system."},
		{ID: "b", Guild: "masonry", Title: "Wire it up", BlockedBy: []string{"a"}},
	}); err != nil {
		t.Fatal(err)
	}
	if approved {
		if _, err := st.DecidePlan(job.ID, true, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	return &stepRig{t: t, r: r, st: st, warden: w, arch: startRun(t, r, RoleArchwright), job: job}
}

func (g *stepRig) start(step string) spool.Response {
	g.n++
	return call(g.t, g.arch, "s"+string(rune('a'+g.n)), "start_step", `{"job":"`+g.job.ID+`","step":"`+step+`"}`)
}

func (g *stepRig) refused(step, code string) {
	g.t.Helper()
	if resp := g.start(step); resp.OK || resp.Error.Code != code {
		g.t.Fatalf("start_step %s: want %s, got %+v", step, code, resp)
	}
}

func (g *stepRig) waitStatus(step, want string) store.StepRun {
	g.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if sr, err := g.st.GetStepRun(g.job.ID, step); err == nil && sr.Status == want {
			return sr
		}
	}
	sr, _ := g.st.GetStepRun(g.job.ID, step)
	g.t.Fatalf("step %s is %q (%s), want %q", step, sr.Status, sr.Reason, want)
	return sr
}

func TestStartStepRunsAWardenThatHandsBack(t *testing.T) {
	g := newStepRig(t, true)
	resp := g.start("a")
	if !resp.OK {
		t.Fatalf("start_step: %+v", resp.Error)
	}
	var info StepInfo
	json.Unmarshal(resp.Output, &info)
	if info.Step != "a" || info.Guild != "masonry" || info.Status != store.StepRunning {
		t.Fatalf("info: %+v", info)
	}
	sr := g.waitStatus("a", store.StepHandedBack)
	if sr.HandbackID == 0 {
		t.Fatalf("no handback recorded: %+v", sr)
	}
	for _, want := range []string{"Build the form", "Use the design system.", "Add login", "with a form", "Write tests first."} {
		if !strings.Contains(sr.Brief, want) {
			t.Fatalf("task brief lacks %q:\n%s", want, sr.Brief)
		}
	}
	if reqs := g.warden.Requests(); len(reqs) != 1 || reqs[0].Step != "a" || reqs[0].Guild != "masonry" || reqs[0].Brief != sr.Brief || reqs[0].Resumed {
		t.Fatalf("warden requests: %+v", reqs)
	}
	if pieces, _ := g.st.Pieces(g.job.ID); len(pieces) != 1 || pieces[0].Status != store.PieceAccepted {
		t.Fatalf("the Warden should have dispatched and accepted a piece: %+v", pieces)
	}
	items, _ := g.st.Inbox(true)
	if len(items) != 1 || items[0].Name != "step_handback" || items[0].JobID != g.job.ID || !strings.Contains(items[0].Summary, "Step a handed back done") {
		t.Fatalf("the handback is not in the Archwright's inbox: %+v", items)
	}
	if !strings.Contains(string(items[0].Payload), `"checked"`) {
		t.Fatalf("handback body should travel in the inbox item as JSON: %s", items[0].Payload)
	}
	if resp := g.start("a"); resp.OK {
		t.Fatal("a handed back step started again")
	}
	if !g.start("b").OK {
		t.Fatal("b is unblocked once a handed back")
	}
	g.waitStatus("b", store.StepHandedBack)
}

func TestStartStepRefusals(t *testing.T) {
	t.Run("an unapproved Plan", func(t *testing.T) {
		g := newStepRig(t, false)
		g.refused("a", spool.CodeConflict)
		if sr, _ := g.st.StepRuns(g.job.ID); len(sr) != 0 {
			t.Fatalf("a refused start left a step: %+v", sr)
		}
	})
	t.Run("a rejected Plan", func(t *testing.T) {
		g := newStepRig(t, false)
		if _, err := g.st.DecidePlan(g.job.ID, false, time.Time{}); err != nil {
			t.Fatal(err)
		}
		g.refused("a", spool.CodeConflict)
	})
	t.Run("a paused Job", func(t *testing.T) {
		g := newStepRig(t, true)
		if _, err := g.r.PauseJob(g.job.ID); err != nil {
			t.Fatal(err)
		}
		g.refused("a", spool.CodeConflict)
	})
	t.Run("a parked Job", func(t *testing.T) {
		g := newStepRig(t, true)
		if _, err := g.st.SaveHandback(g.job.ID, "partial", json.RawMessage(`{}`), nil, "partial handback"); err != nil {
			t.Fatal(err)
		}
		g.refused("a", spool.CodeConflict)
	})
	t.Run("a closed Job", func(t *testing.T) {
		g := newStepRig(t, true)
		if _, err := g.r.CancelJob(g.job.ID); err != nil {
			t.Fatal(err)
		}
		g.refused("a", spool.CodeConflict)
	})
	t.Run("a step with an unfinished blocker", func(t *testing.T) {
		g := newStepRig(t, true)
		g.refused("b", spool.CodeConflict)
		g.r.ConfigureWarden(NewFakeWarden().HoldUntil(make(chan struct{})))
		if !g.start("a").OK {
			t.Fatal("a should start")
		}
		g.refused("b", spool.CodeConflict) // a is running, not handed back
	})
	t.Run("a step already running", func(t *testing.T) {
		g := newStepRig(t, true)
		g.r.ConfigureWarden(NewFakeWarden().HoldUntil(make(chan struct{})))
		if !g.start("a").OK {
			t.Fatal("a should start")
		}
		g.refused("a", spool.CodeConflict)
	})
	t.Run("an unknown step", func(t *testing.T) {
		g := newStepRig(t, true)
		g.refused("nope", spool.CodeNotFound)
	})
	t.Run("a Job with no Plan", func(t *testing.T) {
		g := newStepRig(t, true)
		j, _ := g.st.CreateJob("bare", "")
		if resp := call(t, g.arch, "x1", "start_step", `{"job":"`+j.ID+`","step":"a"}`); resp.OK || resp.Error.Code != spool.CodeNotFound {
			t.Fatalf("got %+v", resp)
		}
	})
	t.Run("no Warden runner", func(t *testing.T) {
		g := newStepRig(t, true)
		g.r.ConfigureWarden(nil)
		g.refused("a", spool.CodeInvalidInput)
	})
	t.Run("wardens cannot start steps", func(t *testing.T) {
		g := newStepRig(t, true)
		w, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: "masonry"})
		if err != nil {
			t.Fatal(err)
		}
		if resp := call(t, w, "w1", "start_step", `{"job":"`+g.job.ID+`","step":"a"}`); resp.OK || resp.Error.Code != spool.CodeForbidden {
			t.Fatalf("got %+v", resp)
		}
	})
}

func TestStartStepWorksForJigPlans(t *testing.T) {
	g := newStepRig(t, false)
	job, _ := g.st.CreateJob("Jig job", "")
	if _, err := g.st.SetJigPlan(job.ID, "fix", []store.Step{{ID: "only", Guild: "masonry", Title: "Fix it"}}); err != nil {
		t.Fatal(err)
	}
	if resp := call(t, g.arch, "j1", "start_step", `{"job":"`+job.ID+`","step":"only"}`); !resp.OK {
		t.Fatalf("a Jig Plan skips approval, not starting: %+v", resp.Error)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if sr, err := g.st.GetStepRun(job.ID, "only"); err == nil && sr.Status == store.StepHandedBack {
			return
		}
	}
	t.Fatal("the Jig step never handed back")
}

func TestAWardenThatFailsFailsTheStepAndItCanStartAgain(t *testing.T) {
	g := newStepRig(t, true)
	// A Wright result that is not done makes the fake Warden hand back failed.
	g.r.Configure(masonryGuilds, NewFakeRunner(map[string][]WrightResult{"a": {{Status: ResultFailed, Uncertainty: "no"}}}))
	if !g.start("a").OK {
		t.Fatal("a should start")
	}
	sr := g.waitStatus("a", store.StepFailed)
	if sr.HandbackID == 0 {
		t.Fatalf("a failed handback is still a handback: %+v", sr)
	}
	g.refused("b", spool.CodeConflict) // a failed: b stays blocked
}

func TestAWardenThatEndsWithoutHandingBackFailsTheStep(t *testing.T) {
	g := newStepRig(t, true)
	g.r.ConfigureWarden(erroringWarden{})
	if !g.start("a").OK {
		t.Fatal("a should start")
	}
	sr := g.waitStatus("a", store.StepFailed)
	if !strings.Contains(sr.Reason, "boom") {
		t.Fatalf("reason: %q", sr.Reason)
	}
	items, _ := g.st.Inbox(true)
	if len(items) != 1 || items[0].Name != "step_failed" {
		t.Fatalf("inbox: %+v", items)
	}
	g.r.ConfigureWarden(NewFakeWarden())
	if !g.start("a").OK { // a failed step starts again
		t.Fatal("restart refused")
	}
	g.waitStatus("a", store.StepHandedBack)
}

type erroringWarden struct{}

func (erroringWarden) RunWarden(_ context.Context, _ WardenRequest) error {
	return errors.New("boom")
}

func TestAWorksRestartMidStepResummonsTheWarden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r1 := NewRuns(st)
	r1.poll = 5 * time.Millisecond
	r1.Configure(masonryGuilds, NewFakeRunner(nil))
	r1.ConfigureWarden(NewFakeWarden().HoldUntil(make(chan struct{}))) // never lets go: the Works stops mid-step
	job, _ := st.CreateJob("J", "")
	if _, err := st.SetJigPlan(job.ID, "fix", []store.Step{{ID: "a", Guild: "masonry", Title: "Do"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r1.startStep(job.ID, "a"); err != nil {
		t.Fatal(err)
	}
	r1.Close() // a stopping Works leaves the step running
	if sr, _ := st.GetStepRun(job.ID, "a"); sr.Status != store.StepRunning {
		t.Fatalf("step after stop: %+v", sr)
	}
	st.Close()

	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	r2 := NewRuns(st2)
	r2.poll = 5 * time.Millisecond
	defer r2.Close()
	r2.Configure(masonryGuilds, NewFakeRunner(nil))
	w2 := NewFakeWarden()
	r2.ConfigureWarden(w2)
	if err := r2.Resume(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if sr, _ := st2.GetStepRun(job.ID, "a"); sr.Status == store.StepHandedBack {
			reqs := w2.Requests()
			if len(reqs) != 1 || !reqs[0].Resumed || !strings.Contains(reqs[0].Brief, "## Resumed") {
				t.Fatalf("requests: %+v", reqs)
			}
			if items, _ := st2.Inbox(true); len(items) != 1 || items[0].Name != "step_handback" {
				t.Fatalf("inbox: %+v", items)
			}
			return
		}
	}
	t.Fatal("the re-summoned Warden never handed back")
}

func TestHandbackFromANonStepWardenLeavesStepsAlone(t *testing.T) {
	g := newRig(t, nil)
	g.dispatch("p")
	g.ok(g.warden, "accept_piece", `{"piece":"p"}`, nil)
	g.ok(g.warden, "handback", `{"status":"done","results":["r"],"checked":["c"]}`, nil)
	if runs, _ := g.st.StepRuns(g.job.ID); len(runs) != 0 {
		t.Fatalf("steps: %+v", runs)
	}
}

func TestResumeEndsTheRunningStepOfAClosedJob(t *testing.T) {
	g := newStepRig(t, true)
	g.r.ConfigureWarden(NewFakeWarden().HoldUntil(make(chan struct{})))
	if !g.start("a").OK {
		t.Fatal("a should start")
	}
	if err := g.st.CloseJob(g.job.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
	if err := g.r.resumeSteps(); err != nil {
		t.Fatal(err)
	}
	if sr, _ := g.st.GetStepRun(g.job.ID, "a"); sr.Status != store.StepFailed {
		t.Fatalf("step: %+v", sr)
	}
}

// The Warden is an agent session with a shell, not an MCP client (ADR 0014):
// its brief must say how to call the tools, or it looks for MCP tools, finds
// none and ends without handing back.
func TestStepBriefStatesTheToolCallingConvention(t *testing.T) {
	brief := renderStepBrief(store.Job{ID: "j1", Title: "Add login"}, store.Step{ID: "a", Title: "Build the form"}, Guild{Name: "masonry"}, "", "")
	if strings.TrimSpace(wardenMD) == "" || !strings.Contains(brief, strings.TrimSpace(wardenMD)) {
		t.Fatalf("the embedded warden.md is not in the step brief:\n%s", brief)
	}
	for _, want := range []string{
		"`archwright tool NAME`", "JSON on stdin", "JSON on stdout", "Bash", "not MCP",
		`dispatch_wright`, `"piece"`, `"brief"`, `"objective"`, `"scope"`, `"constraints"`, `"inputs"`, `"definition_of_done"`, `"return_shape"`, `"limits"`, `"timeout_minutes"`,
		`inspect_wright`, `rework_wright`, `"reason"`, `"diagnosis"`,
		`accept_piece`,
		`handback`, `"status"`, `"results"`, `"checked"`, `"rejected"`, `"uncertainty"`, `"flags"`, `"evidence"`,
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("step brief lacks %q:\n%s", want, brief)
		}
	}
}

// A step's Inputs are only labels; the brief must name the accepted pieces so
// a reviewing Warden can inspect them.
func TestStepBriefNamesPriorWork(t *testing.T) {
	brief := renderStepBrief(store.Job{ID: "j1", Title: "Fix"}, store.Step{ID: "review", Title: "Review"}, Guild{Name: "hallmark"}, "", "- fix-pagination (masonry Guild)")
	if !strings.Contains(brief, "## Work so far") || !strings.Contains(brief, "fix-pagination") {
		t.Fatalf("brief lacks the prior work:\n%s", brief)
	}
	if none := renderStepBrief(store.Job{ID: "j1"}, store.Step{ID: "a"}, Guild{Name: "m"}, "", ""); strings.Contains(none, "Work so far") {
		t.Fatalf("empty prior work rendered:\n%s", none)
	}
}

func TestPriorWorkNamesAcceptedPiecesAndIntegrationBranch(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	if got, err := g.r.priorWork(g.job); err != nil || got != "" {
		t.Fatalf("nothing accepted yet: %q, %v", got, err)
	}
	g.dispatch("fix-pagination")
	g.ok(g.warden, "accept_piece", `{"piece":"fix-pagination"}`, nil)
	got, err := g.r.priorWork(g.job)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "git diff") {
		t.Fatalf("brief tells a Warden to run git diff:\n%s", got)
	}
	for _, want := range []string{"fix-pagination", "inspect_wright", g.worktree.Branch} {
		if !strings.Contains(got, want) {
			t.Fatalf("prior work lacks %q:\n%s", want, got)
		}
	}
}
