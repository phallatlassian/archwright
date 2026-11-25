package works

import (
	"context"
	"sync"
	"testing"

	"github.com/phallatlassian/archwright/internal/forge"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// The rig's Site is registered by https://git.example.com/o/r.git.
const (
	sitePR      = "https://git.example.com/o/r/pull/12"
	recordArgs  = `{"url":"` + sitePR + `","number":12}`
	otherRepoPR = "https://git.example.com/o/other/pull/12"
)

// No test reaches a real Forge: a Runs with no Forge set answers "unsupported".
func init() { defaultForge = func() forge.Forge { return forge.Unsupported{} } }

// fakeForge answers ReadPR with a canned pull request or error and records
// what it was asked. It is the only Forge a test ever reaches.
type fakeForge struct {
	mu    sync.Mutex
	pr    forge.PullRequest
	err   error
	reads []string
}

func (f *fakeForge) ReadPR(_ context.Context, u string) (forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, u)
	if f.err != nil {
		return forge.PullRequest{}, f.err
	}
	pr := f.pr
	pr.URL = u
	return pr, nil
}

func (f *fakeForge) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reads)
}

func (f *fakeForge) set(pr forge.PullRequest, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pr, f.err = pr, err
}

// planWithOpenPR gives the rig's Job a Plan whose last step is open-pr and, if
// start is set, starts that step so it is running.
func (g *gateRig) planWithOpenPR(start bool) {
	g.t.Helper()
	if _, err := g.st.SetPlan(g.job.ID, []store.Step{{ID: "build", Guild: "masonry", Title: "Build"}, {ID: OpenPRStep, Guild: "masonry", Title: "Open the PR", BlockedBy: []string{"build"}}}); err != nil {
		g.t.Fatal(err)
	}
	if !start {
		return
	}
	_, err := g.st.StartStep(g.job.ID, OpenPRStep, func(store.Plan, []store.StepRun) (string, string, error) { return "masonry", "open the PR", nil })
	if err != nil {
		g.t.Fatal(err)
	}
}

// wardenFor starts a run of the rig's Job carrying out the given step.
func (g *gateRig) wardenFor(role, task string) RunEnv {
	g.t.Helper()
	env, err := g.r.StartFor(role, g.t.TempDir(), RunContext{Job: g.job.ID, Guild: "masonry", Task: task})
	if err != nil {
		g.t.Fatal(err)
	}
	return env
}

// recordRig is a rig whose Job is at its running open-pr step, with a fake
// Forge for the Works' own reads.
func recordRig(t *testing.T) (*gateRig, *fakeForge, RunEnv) {
	t.Helper()
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	g.planWithOpenPR(true)
	return g, f, g.wardenFor(RoleWarden, OpenPRStep)
}

func TestRecordPRStoresTheJobsPRAfterCheckingItAgainstTheSitesRemote(t *testing.T) {
	g, _, env := recordRig(t)
	var out PRRecorded
	g.ok(env, "record_pr", recordArgs, &out)
	if out.Status != "recorded" || out.URL != sitePR || out.Number != 12 {
		t.Fatalf("%+v", out)
	}
	job, _ := g.st.GetJob(g.job.ID)
	if job.PR != sitePR {
		t.Fatalf("recorded PR %q", job.PR)
	}
	evs, _ := g.st.Events(g.job.ID)
	n := 0
	for _, e := range evs {
		if e.Type == "job.pr_recorded" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("job.pr_recorded events: %d", n)
	}
}

func TestRecordPRIsOnePerJobAndRepeatingItIsHarmless(t *testing.T) {
	g, _, env := recordRig(t)
	g.ok(env, "record_pr", recordArgs, nil)
	var again PRRecorded
	g.ok(env, "record_pr", recordArgs, &again)
	if again.Status != "exists" || again.URL != sitePR {
		t.Fatalf("%+v", again)
	}
	g.ok(g.warden, "record_pr", recordArgs, nil) // the same PR from any Warden of the Job, even after the step
	g.fails(env, "record_pr", `{"url":"https://git.example.com/o/r/pull/13","number":13}`, spool.CodeConflict)
	if job, _ := g.st.GetJob(g.job.ID); job.PR != sitePR {
		t.Fatalf("the Job's PR changed to %q", job.PR)
	}
	evs, _ := g.st.Events(g.job.ID)
	n := 0
	for _, e := range evs {
		if e.Type == "job.pr_recorded" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("job.pr_recorded events: %d", n)
	}
}

func TestRecordPRRefusesAPRThatIsNotOnTheSitesRemote(t *testing.T) {
	g, f, env := recordRig(t)
	for name, in := range map[string]string{
		"another repository": `{"url":"` + otherRepoPR + `","number":12}`,
		"another owner":      `{"url":"https://git.example.com/x/r/pull/12","number":12}`,
		"another host":       `{"url":"https://github.com/o/r/pull/12","number":12}`,
		"a look-alike host":  `{"url":"https://git.example.com.evil.test/o/r/pull/12","number":12}`,
		"another port":       `{"url":"https://git.example.com:8443/o/r/pull/12","number":12}`,
		"number off the URL": `{"url":"` + sitePR + `","number":13}`,
		"no number":          `{"url":"` + sitePR + `"}`,
		"zero number":        `{"url":"` + sitePR + `","number":0}`,
		"no url":             `{"number":12}`,
		"an issue":           `{"url":"https://git.example.com/o/r/issues/12","number":12}`,
		"a trailing path":    `{"url":"` + sitePR + `/files","number":12}`,
		"a query":            `{"url":"` + sitePR + `?x=1","number":12}`,
		"an option":          `{"url":"--web","number":12}`,
		"an unknown field":   `{"url":"` + sitePR + `","number":12,"title":"t"}`,
		"a string number":    `{"url":"` + sitePR + `","number":"12"}`,
		"not an object":      `[]`,
	} {
		if resp := g.call(env, "record_pr", in); resp.OK || resp.Error.Code != spool.CodeInvalidInput {
			t.Errorf("%s: %+v", name, resp)
		}
	}
	if job, _ := g.st.GetJob(g.job.ID); job.PR != "" {
		t.Fatalf("a PR was recorded: %q", job.PR)
	}
	if f.readCount() != 0 {
		t.Fatal("the Forge was reached")
	}
}

func TestRecordPRMatchesTheRemoteWhateverItsSpelling(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	g.r.UseForge(&fakeForge{})
	g.planWithOpenPR(true)
	env := g.wardenFor(RoleWarden, OpenPRStep)
	g.ok(env, "record_pr", `{"url":"https://GIT.example.com/o/R/pull/12","number":12}`, nil)
}

func TestRecordPRIsForTheWardenOfTheRunningOpenPRStep(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	g.r.UseForge(&fakeForge{})
	g.fails(g.warden, "record_pr", recordArgs, spool.CodeConflict) // no Plan
	g.planWithOpenPR(false)
	g.fails(g.wardenFor(RoleWarden, OpenPRStep), "record_pr", recordArgs, spool.CodeConflict) // step pending
	if _, err := g.st.StartStep(g.job.ID, OpenPRStep, func(store.Plan, []store.StepRun) (string, string, error) { return "masonry", "open", nil }); err != nil {
		t.Fatal(err)
	}
	g.fails(g.wardenFor(RoleWarden, "build"), "record_pr", recordArgs, spool.CodeConflict) // another step's Warden
	if err := g.st.FailStep(g.job.ID, OpenPRStep, "stopped"); err != nil {
		t.Fatal(err)
	}
	g.fails(g.wardenFor(RoleWarden, OpenPRStep), "record_pr", recordArgs, spool.CodeConflict)
	if job, _ := g.st.GetJob(g.job.ID); job.PR != "" {
		t.Fatalf("a PR was recorded: %q", job.PR)
	}
}

func TestRecordPRIsForTheWardenRoleOnly(t *testing.T) {
	g, _, _ := recordRig(t)
	for _, role := range []string{RoleWright, RoleArchwright} {
		g.fails(g.wardenFor(role, OpenPRStep), "record_pr", recordArgs, spool.CodeForbidden)
	}
	rev, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: "hallmark", Review: "rv1", Task: OpenPRStep})
	if err != nil {
		t.Fatal(err)
	}
	g.fails(rev, "record_pr", recordArgs, spool.CodeInvalidInput) // a review is read-only
	if job, _ := g.st.GetJob(g.job.ID); job.PR != "" {
		t.Fatalf("a PR was recorded: %q", job.PR)
	}
}

func TestRecordPRRefusesAClosedJobAndAJobWithNoSite(t *testing.T) {
	g, _, env := recordRig(t)
	if err := g.st.CloseJob(g.job.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
	g.fails(env, "record_pr", recordArgs, spool.CodeConflict)

	noSite := newRig(t, nil)
	noSite.r.UseForge(&fakeForge{})
	env2, err := noSite.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: noSite.job.ID, Guild: "masonry", Task: OpenPRStep})
	if err != nil {
		t.Fatal(err)
	}
	noSite.fails(env2, "record_pr", recordArgs, spool.CodeConflict) // no open-pr step running at all
}

// The Forge tools the Works once brokered, and merge_base, are gone: no role
// reaches any of them (ADR 0028).
func TestTheBrokeredForgeToolsAreGone(t *testing.T) {
	g, f, env := recordRig(t)
	for _, name := range removedForgeTools {
		if _, ok := catalogue[name]; ok {
			t.Errorf("%s is still in the catalogue", name)
		}
		for _, e := range []RunEnv{env, g.warden, g.wardenFor(RoleWright, OpenPRStep), g.arch} {
			g.fails(e, name, `{}`, spool.CodeUnknownTool)
		}
	}
	if f.readCount() != 0 {
		t.Fatal("the Forge was reached")
	}
}
