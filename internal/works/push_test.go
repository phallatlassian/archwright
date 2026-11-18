package works

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/forge"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

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

// wardenFor starts a Warden run of the rig's Job carrying out the given step.
func (g *gateRig) wardenFor(role, task string) RunEnv {
	g.t.Helper()
	env, err := g.r.StartFor(role, g.t.TempDir(), RunContext{Job: g.job.ID, Guild: "masonry", Task: task})
	if err != nil {
		g.t.Fatal(err)
	}
	return env
}

func pushed(g *gateRig, f *fakeForge) forge.PushRequest {
	g.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pushes) != 1 {
		g.t.Fatalf("pushes: %+v", f.pushes)
	}
	return f.pushes[0]
}

func TestPushIsRefusedBeforeTheOpenPRStepAndNeverReachesTheForge(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)

	// No Plan, a Plan whose open-pr step is pending, and a Warden of another step.
	g.fails(g.warden, "push_branch", `{}`, spool.CodeConflict)
	g.planWithOpenPR(false)
	g.fails(g.wardenFor(RoleWarden, OpenPRStep), "push_branch", `{}`, spool.CodeConflict)
	g.fails(g.wardenFor(RoleWarden, "build"), "push_branch", `{}`, spool.CodeConflict)
	g.fails(g.warden, "push_branch", `{}`, spool.CodeConflict)
	if f.pushCount() != 0 {
		t.Fatalf("the Forge was reached: %+v", f.pushes)
	}
}

func TestPushIsRefusedForWrightsAndTheArchwright(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	g.planWithOpenPR(true)
	for _, role := range []string{RoleWright, RoleArchwright} {
		g.fails(g.wardenFor(role, OpenPRStep), "push_branch", `{}`, spool.CodeForbidden)
	}
	if f.pushCount() != 0 {
		t.Fatalf("the Forge was reached: %+v", f.pushes)
	}
}

func TestPushNeedsTheRunToken(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	g.planWithOpenPR(true)
	env := g.wardenFor(RoleWarden, OpenPRStep)
	env.Token = "not-the-token"
	if resp := call(t, env, "r1", "push_branch", `{}`); resp.OK || resp.Error.Code != spool.CodeUnauthorized || f.pushCount() != 0 {
		t.Fatalf("%+v", resp)
	}
}

func TestPushDuringTheOpenPRStepPushesTheJobsIntegrationBranch(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	g.planWithOpenPR(true)
	var out Pushed
	g.ok(g.wardenFor(RoleWarden, OpenPRStep), "push_branch", `{}`, &out)
	if out.Status != "pushed" || out.Branch != g.worktree.Branch {
		t.Fatalf("%+v", out)
	}
	req := pushed(g, f)
	site, _ := g.st.GetSite("demo")
	want := forge.PushRequest{SiteURL: site.URL, ClonePath: site.ClonePath, Branch: g.worktree.Branch, DefaultBranch: "main"}
	if req != want {
		t.Fatalf("push\n got %+v\nwant %+v", req, want)
	}
	if req.Branch == req.DefaultBranch || !strings.HasSuffix(req.Branch, "/integration") {
		t.Fatalf("not the integration branch: %+v", req)
	}
}

func TestPushTakesNoArgument(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	g.planWithOpenPR(true)
	env := g.wardenFor(RoleWarden, OpenPRStep)
	for i, in := range []string{`{"branch":"main"}`, `{"force":true}`, `{"remote":"x"}`, `{"refspec":"+a:b"}`, `[]`} {
		resp := call(t, env, "arg"+string(rune('a'+i)), "push_branch", in)
		if resp.OK || resp.Error.Code != spool.CodeInvalidInput {
			t.Fatalf("%s: %+v", in, resp)
		}
	}
	if f.pushCount() != 0 {
		t.Fatal("the Forge was reached with an argument the tool does not take")
	}
}

func TestPushIsAllowedWhileThePRIsOpenAndRefusedOnceTheJobIsClosed(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	g.planWithOpenPR(false)
	// A Warden of any step, even none (a conflict-fix Warden), may push while the PR is open.
	g.fails(g.warden, "push_branch", `{}`, spool.CodeConflict)
	if err := g.st.RecordPR(g.job.ID, "https://github.com/o/r/pull/9"); err != nil {
		t.Fatal(err)
	}
	g.ok(g.warden, "push_branch", `{}`, nil)
	g.ok(g.wardenFor(RoleWarden, "build"), "push_branch", `{}`, nil)
	g.ok(g.warden, "push_branch", `{}`, nil) // a re-push
	if f.pushCount() != 3 {
		t.Fatalf("pushes: %d", f.pushCount())
	}
	if err := g.st.CloseJob(g.job.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	g.fails(g.warden, "push_branch", `{}`, spool.CodeConflict)
	if f.pushCount() != 3 {
		t.Fatalf("a closed Job pushed: %d", f.pushCount())
	}
}

func TestPushIsRefusedAfterTheOpenPRStepEndsWithNoPR(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	g.planWithOpenPR(true)
	if err := g.st.FailStep(g.job.ID, OpenPRStep, "stopped"); err != nil {
		t.Fatal(err)
	}
	g.fails(g.wardenFor(RoleWarden, OpenPRStep), "push_branch", `{}`, spool.CodeConflict)
	if f.pushCount() != 0 {
		t.Fatal("pushed from a step that is no longer running")
	}
}

func TestPushRefusesAReviewRunAndASitelessJob(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	g.planWithOpenPR(true)
	if err := g.st.RecordPR(g.job.ID, "https://github.com/o/r/pull/9"); err != nil {
		t.Fatal(err)
	}
	rev, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: "hallmark", Review: "rev-1"})
	if err != nil {
		t.Fatal(err)
	}
	g.fails(rev, "push_branch", `{}`, spool.CodeInvalidInput)

	job, err := g.st.CreateJob("No site", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.st.RecordPR(job.ID, "https://github.com/o/r/pull/10"); err != nil {
		t.Fatal(err)
	}
	env, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: job.ID, Guild: "masonry"})
	if err != nil {
		t.Fatal(err)
	}
	g.fails(env, "push_branch", `{}`, spool.CodeInvalidInput)
	if f.pushCount() != 0 {
		t.Fatal("the Forge was reached")
	}
}

func TestPushForgeFaultsAreStructuredAndAuthNetworkRaiseNeedsYou(t *testing.T) {
	for _, c := range []struct {
		err   error
		code  string
		needs bool
	}{
		{&forge.Error{Kind: forge.KindAuth, Detail: "no creds"}, spool.CodeForgeFault, true},
		{&forge.Error{Kind: forge.KindNetwork, Detail: "offline"}, spool.CodeForgeFault, true},
		{&forge.Error{Kind: forge.KindOther, Detail: "non-fast-forward"}, spool.CodeForgeFault, false},
		{forge.ErrUnsupported, spool.CodeUnsupported, false},
	} {
		g := newGateRig(t, store.Gates{})
		f := &fakeForge{pushed: c.err}
		g.r.UseForge(f)
		g.planWithOpenPR(true)
		resp := g.call(g.wardenFor(RoleWarden, OpenPRStep), "push_branch", `{}`)
		if resp.OK || resp.Error.Code != c.code || (c.code == spool.CodeForgeFault && !strings.Contains(resp.Error.Message, "no automatic retry")) {
			t.Fatalf("%v: %+v", c.err, resp)
		}
		items, _ := g.st.NeedsYou()
		if c.needs != (len(items) == 1) || f.pushCount() != 1 {
			t.Fatalf("%v: Needs-you %+v, pushes %d", c.err, items, f.pushCount())
		}
	}
}

// The whole path against a local bare remote: the real adapter, the real clone,
// the Job's real integration branch. fakeHost maps the Site's URL onto a local
// bare repository, so nothing leaves the machine.
func TestPushReachesALocalBareRemoteAsAFastForwardAndNeverTheDefaultBranch(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	g.r.UseForge(forge.ByHost{Adapters: map[string]forge.Forge{"git.example.com": forge.GH{}}})
	remote := resolveLocal(t)
	mainBefore := git(t, remote, "rev-parse", "main")

	g.planWithOpenPR(true)
	env := g.wardenFor(RoleWarden, OpenPRStep)
	first := g.commit("a.txt", "a")
	g.ok(env, "push_branch", `{}`, nil)
	if got := git(t, remote, "rev-parse", g.worktree.Branch); got != first {
		t.Fatalf("remote %s, want %s", got, first)
	}

	if err := g.st.RecordPR(g.job.ID, "https://git.example.com/o/r/pull/1"); err != nil {
		t.Fatal(err)
	}
	second := g.commit("b.txt", "b")
	g.ok(g.warden, "push_branch", `{}`, nil) // a fast-forward re-push while the PR is open
	if got := git(t, remote, "rev-parse", g.worktree.Branch); got != second {
		t.Fatalf("remote %s, want %s", got, second)
	}

	git(t, g.worktree.Path, "reset", "--hard", "HEAD~2") // rewritten history is refused by the remote
	g.commit("c.txt", "c")
	resp := g.call(g.warden, "push_branch", `{}`)
	if resp.OK || resp.Error.Code != spool.CodeForgeFault || !strings.Contains(resp.Error.Message, "non-fast-forward") {
		t.Fatalf("%+v", resp)
	}
	if got := git(t, remote, "rev-parse", g.worktree.Branch); got != second {
		t.Fatalf("the remote branch was rewritten to %s", got)
	}
	if got := git(t, remote, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("the default branch moved: %s -> %s", mainBefore, got)
	}
}

// resolveLocal finds the bare repository the Site's URL is mapped onto.
func resolveLocal(t *testing.T) string {
	t.Helper()
	base := os.Getenv("GIT_CONFIG_KEY_0")
	dir := strings.TrimSuffix(strings.TrimPrefix(base, "url."), "/.insteadOf")
	p := filepath.Join(dir, "o", "r.git")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}
