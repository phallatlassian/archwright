package works

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/store"
)

// endingWarden records each run and ends it, so the step fails; the tests
// only look at where the Warden started.
type endingWarden struct {
	*FakeWarden
}

func (h endingWarden) RunWarden(ctx context.Context, req WardenRequest) error {
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.mu.Unlock()
	return nil
}

// newSiteStepRig is newStepRig for a Job on a registered Site, with the
// shipped Guilds and their skills delivered.
func newSiteStepRig(t *testing.T, steps []store.Step) *stepRig {
	t.Helper()
	g := newStepRig(t, false)
	url := fakeHost(t, map[string]string{"a.txt": "a\n"})
	root, _ := filepath.EvalSymlinks(t.TempDir())
	sm := sites.New(g.st, filepath.Join(root, "worktrees"), filepath.Join(root, "clones"))
	if _, err := sm.Register(sites.RegisterInput{Name: "demo", URL: url, BaseBranch: "main", Gates: &store.Gates{Test: "true"}}); err != nil {
		t.Fatal(err)
	}
	g.r.UseSites(sm)
	g.r.Configure(func(name string) (Guild, error) { return Guild{Name: name, Standards: "Be careful."}, nil }, NewFakeRunner(nil))
	g.warden = &FakeWarden{}
	g.r.ConfigureWarden(endingWarden{g.warden})
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g.r.UseDefinitions(reg)
	g.r.UseSkills(SkillsDelivery{Catalogue: skills.Vendored(), Runner: "claude", RunDir: func(id string) string { return filepath.Join(root, "runs", id) }})
	job, err := g.st.CreateJobOn("demo", "On a Site", "")
	if err != nil {
		t.Fatal(err)
	}
	g.job = job
	if _, err := g.st.SetPlan(job.ID, steps); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.DecidePlan(job.ID, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *stepRig) wardenWorktree(step string) store.Worktree {
	g.t.Helper()
	if resp := g.start(step); !resp.OK {
		g.t.Fatalf("start %s: %+v", step, resp)
	}
	g.waitStatus(step, store.StepFailed)
	reqs := g.warden.Requests()
	cwd := reqs[len(reqs)-1].Worktree
	wts, err := g.st.Worktrees(g.job.ID)
	if err != nil {
		g.t.Fatal(err)
	}
	for _, w := range wts {
		if w.Path == cwd {
			return w
		}
	}
	g.t.Fatalf("the Warden of %s works in %s, which is none of the Job's worktrees %+v", step, cwd, wts)
	return store.Worktree{}
}

func TestStepWardensStartInWorktreesOfTheMainCheckout(t *testing.T) {
	g := newSiteStepRig(t, []store.Step{
		{ID: "build", Guild: "masonry", Title: "Build"},
		{ID: "build2", Guild: "masonry", Title: "Build more"},
		{ID: "look", Guild: "survey", Title: "Look"},
		{ID: "peek", Guild: "masonry", Title: "Peek", Worktree: "detached"},
		{ID: "own", Guild: "masonry", Title: "Own branch", Worktree: "branch"},
	})
	one := g.wardenWorktree("build")
	if one.Kind != store.KindIntegration {
		t.Errorf("Masonry's Warden works in a %s worktree", one.Kind)
	}
	if _, err := os.Stat(filepath.Join(one.Path, "a.txt")); err != nil {
		t.Error("the integration worktree is not a full checkout")
	}
	if _, err := os.Stat(filepath.Join(one.Path, ".claude/skills/pr/SKILL.md")); err != nil {
		t.Errorf("the Warden cannot see the pr skill: %v", err)
	}
	if env := g.warden.Requests()[0].ExtraEnv["GIT_CONFIG_GLOBAL"]; env == "" {
		t.Error("no GIT_CONFIG_GLOBAL for the step Warden")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", g.warden.Requests()[0].ExtraEnv["GIT_CONFIG_GLOBAL"])
	if out := git(t, one.Path, "status", "--porcelain"); out != "" {
		t.Errorf("delivered skills show in git status: %q", out)
	}
	if common := git(t, one.Path, "rev-parse", "--git-common-dir"); !strings.Contains(common, "clones") {
		t.Errorf("not a worktree of the Site's clone: %s", common)
	}

	// Two Wardens asking for integration in one Job share its one worktree.
	if two := g.wardenWorktree("build2"); two.ID != one.ID {
		t.Errorf("integration worktrees %s and %s are not shared", one.ID, two.ID)
	}

	look := g.wardenWorktree("look")
	if look.Kind != store.KindEmpty {
		t.Errorf("Survey's Warden works in a %s worktree", look.Kind)
	}
	if _, err := os.Stat(filepath.Join(look.Path, "a.txt")); err == nil {
		t.Error("the empty worktree holds the Site's files")
	}
	if entries, _ := os.ReadDir(look.Path); len(entries) > 3 { // .git and the delivered .agents and .claude
		t.Errorf("the empty worktree holds %v", entries)
	}
	if out := git(t, look.Path, "status", "--porcelain"); out != "" {
		t.Errorf("empty worktree's git status is not quiet: %q", out)
	}

	if peek := g.wardenWorktree("peek"); peek.Kind != store.KindReadOnly {
		t.Errorf("a step's detached override gave a %s worktree", peek.Kind)
	}
	own := g.wardenWorktree("own")
	if own.Kind != store.KindWarden || own.Branch == "" {
		t.Errorf("a step's branch override gave %+v", own)
	}
}

func TestWideningStepWorktreeIsRefused(t *testing.T) {
	g := newSiteStepRig(t, []store.Step{{ID: "a", Guild: "hallmark", Title: "Review"}})
	// set_plan refuses it up front.
	resp := call(g.t, g.arch, "sp", "set_plan", `{"job_id":"`+g.job.ID+`","steps":[{"id":"a","guild":"survey","title":"Look","worktree":"integration"}]}`)
	if resp.OK || !strings.Contains(resp.Error.Message, "narrow") {
		t.Errorf("set_plan accepted a widening worktree: %+v", resp)
	}
	resp = call(g.t, g.arch, "sp2", "set_plan", `{"job_id":"`+g.job.ID+`","steps":[{"id":"a","guild":"survey","title":"Look","worktree":"everywhere"}]}`)
	if resp.OK {
		t.Errorf("set_plan accepted an unknown worktree: %+v", resp)
	}
	// A Plan stored some other way still fails closed when the step starts.
	if _, err := g.st.SetPlan(g.job.ID, []store.Step{{ID: "a", Guild: "hallmark", Title: "Review", Worktree: "branch"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.DecidePlan(g.job.ID, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if resp := g.start("a"); !resp.OK {
		t.Fatalf("start: %+v", resp)
	}
	sr := g.waitStatus("a", store.StepFailed)
	if !strings.Contains(sr.Reason, "narrow") || len(g.warden.Requests()) != 0 {
		t.Errorf("reason %q, Warden requests %d", sr.Reason, len(g.warden.Requests()))
	}
}
