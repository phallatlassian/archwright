package works

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/runner"
	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// wardenRig runs task Wardens through the real AgentWarden, with the stub CLIs
// of the runner package standing in for claude and codex.
type wardenRig struct {
	*agentRig
	arch RunEnv
	n    int
}

func newWardenRig(t *testing.T, fixture string) *wardenRig {
	t.Helper()
	g := newAgentRig(t, fixture)
	td, _ := filepath.Abs("../runner/testdata")
	env := map[string]string{EnvClaudeBinary: filepath.Join(td, "stub-claude.sh"), EnvCodexBinary: filepath.Join(td, "stub-codex.sh")}
	guilds := func(name string) (Guild, error) {
		if gd, ok := agentGuilds[name]; ok {
			return gd, nil
		}
		return Guild{}, errInvalidInput
	}
	w, rv := WardenRunnersFrom(func(k string) string { return env[k] }, guilds, g.st)
	g.r.ConfigureWarden(w)
	g.r.ConfigureReview(rv)
	if _, err := g.st.SetPlan(g.job.ID, []store.Step{
		{ID: "a", Guild: "masonry", Title: "Build it"},
		{ID: "b", Guild: "carvers", Title: "Carve it"},
		{ID: "c", Guild: "lockedout", Title: "Lock it"},
		{ID: "d", Guild: "surveyors", Title: "Look at it"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.DecidePlan(g.job.ID, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	return &wardenRig{agentRig: g, arch: startRun(t, g.r, RoleArchwright)}
}

func (g *wardenRig) startStep(step string) spool.Response {
	g.n++
	return call(g.t, g.arch, "w"+string(rune('a'+g.n)), "start_step", `{"job":"`+g.job.ID+`","step":"`+step+`"}`)
}

func (g *wardenRig) stepEnds(step string) store.StepRun {
	g.t.Helper()
	var sr store.StepRun
	eventually(g.t, "step "+step+" to end", func() bool {
		sr, _ = g.st.GetStepRun(g.job.ID, step)
		return sr.Status != store.StepRunning
	})
	return sr
}

// runRecord finds the run record of a Job's only Warden run.
func (g *wardenRig) runRecord() store.RunRecord {
	g.t.Helper()
	var recs []store.RunRecord
	eventually(g.t, "the run record to end", func() bool {
		recs, _ = g.st.RunRecords(g.job.ID)
		return len(recs) == 1 && recs[0].Status != "running"
	})
	return recs[0]
}

func TestTaskWardenRunsOnClaudeInAPerRunDirectoryAndTheRunIsRecorded(t *testing.T) {
	g := newWardenRig(t, "success.jsonl")
	if resp := g.startStep("a"); !resp.OK {
		t.Fatalf("start_step: %+v", resp.Error)
	}
	sr := g.stepEnds("a")
	if sr.Status != store.StepFailed || !strings.Contains(sr.Reason, "ended without handing back") {
		t.Fatalf("a Warden that never hands back must fail its step visibly: %+v", sr)
	}
	// It ran in a per-run directory outside any Site, not in a worktree.
	pwd := strings.TrimSpace(g.file("pwd"))
	if wts, _ := g.st.Worktrees(g.job.ID); len(wts) != 1 || strings.HasPrefix(pwd, filepath.Dir(wts[0].Path)) {
		t.Errorf("ran in %q beside the Site worktrees %+v", pwd, wts)
	}
	if !strings.Contains(pwd, "warden-") {
		t.Errorf("ran in %q, want a per-run directory", pwd)
	}
	args := g.file("args")
	if !strings.Contains(args, "stream-json") || !strings.Contains(args, "--permission-mode\ndefault") || strings.Contains(args, "bypass") {
		t.Errorf("args = %q", args)
	}
	if env := g.file("env"); !strings.Contains(env, "ARCHWRIGHT_RUN=") || !strings.Contains(env, "ARCHWRIGHT_SPOOL=") {
		t.Errorf("env = %q", env)
	}
	if !strings.Contains(args, "Build it") { // the step's task brief is the prompt
		t.Errorf("brief missing from %q", args)
	}
	rec := g.runRecord()
	if rec.Runner != "claude" || rec.InputTokens != 120 || rec.Status != "done" || rec.Guild != "masonry" || rec.DispatchID != "" {
		t.Errorf("run record = %+v", rec)
	}
	evs, _ := g.st.RunEvents(rec.RunID)
	if len(evs) < 5 || evs[0].Kind != "posture" || !evs[0].HasRaw {
		t.Errorf("timeline = %+v", evs)
	}
}

// Plan mode would also stop the Warden calling `archwright tool`, so a
// read-only Guild is held read-only by denying the edit tools instead.
func TestTaskWardenOfAReadOnlyGuildDeniesEditToolsNotPlanMode(t *testing.T) {
	g := newWardenRig(t, "success.jsonl")
	g.startStep("d")
	g.stepEnds("d")
	args := g.file("args")
	if strings.Contains(args, "--permission-mode\nplan") || !strings.Contains(args, "--disallowed-tools\nEdit,Write,MultiEdit,NotebookEdit") {
		t.Errorf("args = %q", args)
	}
}

func TestTaskWardenOnCodexAndItsFailureReasonIsVisible(t *testing.T) {
	g := newWardenRig(t, "codex-failed.jsonl")
	if resp := g.startStep("b"); !resp.OK {
		t.Fatalf("start_step: %+v", resp.Error)
	}
	sr := g.stepEnds("b")
	if sr.Status != store.StepFailed || !strings.Contains(sr.Reason, "codex run failed") {
		t.Fatalf("step = %+v", sr)
	}
	if args := g.file("args"); !strings.HasPrefix(args, "exec\n") {
		t.Errorf("not run on codex: %q", args)
	}
	if rec := g.runRecord(); rec.Status != "failed" || rec.Runner != "codex" {
		t.Errorf("run record = %+v", rec)
	}
}

func TestTaskWardenOfAGuildThatDeniesToolsFailsClosedOnCodex(t *testing.T) {
	g := newWardenRig(t, "codex-success.jsonl")
	if resp := g.startStep("c"); !resp.OK {
		t.Fatalf("start_step: %+v", resp.Error)
	}
	sr := g.stepEnds("c")
	if sr.Status != store.StepFailed || !strings.Contains(sr.Reason, "cannot enforce") {
		t.Fatalf("step = %+v", sr)
	}
	if g.file("args") != "" {
		t.Error("the runner was spawned")
	}
}

func TestTaskWardenThatTriesToWidenPermissionsIsStopped(t *testing.T) {
	g := newWardenRig(t, "widened.jsonl")
	g.startStep("a")
	sr := g.stepEnds("a")
	if sr.Status != store.StepFailed || !strings.Contains(sr.Reason, "the Warden failed") {
		t.Fatalf("step = %+v", sr)
	}
}

func TestAWardenRequestThatWouldWidenPermissionsIsRefusedBeforeSpawn(t *testing.T) {
	g := newAgentRig(t, "success.jsonl")
	td, _ := filepath.Abs("../runner/testdata")
	w := &AgentWarden{Agent: &AgentRunner{
		Runners: map[string]runner.Runner{"claude": &runner.Claude{Binary: filepath.Join(td, "stub-claude.sh")}},
		Guilds:  func(string) (Guild, error) { return agentGuilds["masonry"], nil },
	}}
	err := w.RunWarden(context.Background(), WardenRequest{Guild: "masonry", Worktree: t.TempDir(), Brief: "b", ExtraEnv: map[string]string{"CLAUDE_CONFIG_DIR": "/elsewhere"}})
	if !errors.Is(err, runner.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	if g.file("args") != "" {
		t.Error("the runner was spawned")
	}
	if err := w.RunWarden(context.Background(), WardenRequest{Guild: "masonry", Brief: "b"}); err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Fatalf("a Warden with no workspace must not run: %v", err)
	}
}

func TestTaskWardenRunsForAJobWithNoSite(t *testing.T) {
	g := newWardenRig(t, "success.jsonl")
	job, _ := g.st.CreateJob("No site", "")
	g.st.SetPlan(job.ID, []store.Step{{ID: "a", Guild: "masonry", Title: "Build it"}})
	g.st.DecidePlan(job.ID, true, time.Time{})
	if resp := call(t, g.arch, "ns", "start_step", `{"job":"`+job.ID+`","step":"a"}`); !resp.OK {
		t.Fatalf("start_step: %+v", resp.Error)
	}
	var sr store.StepRun
	eventually(t, "the step to end", func() bool { sr, _ = g.st.GetStepRun(job.ID, "a"); return sr.Status != store.StepRunning })
	if !strings.Contains(sr.Reason, "ended without handing back") || g.file("args") == "" {
		t.Fatalf("the Warden should have run: %+v", sr)
	}
}

func TestTaskWardenGetsItsGuildsSkills(t *testing.T) {
	g := newWardenRig(t, "success.jsonl")
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g.r.UseDefinitions(reg)
	runs, _ := filepath.EvalSymlinks(t.TempDir())
	g.r.UseSkills(SkillsDelivery{Catalogue: skills.Vendored(), RunDir: func(run string) string { return filepath.Join(runs, run) }})
	g.startStep("a")
	g.stepEnds("a")
	pwd := strings.TrimSpace(g.file("pwd"))
	if _, err := os.Stat(filepath.Join(pwd, ".claude/skills/tdd/SKILL.md")); err != nil {
		t.Errorf("the Warden's worktree has no skills: %v", err)
	}
	if env := g.file("env"); !strings.Contains(env, "GIT_CONFIG_GLOBAL=") {
		t.Errorf("the Warden did not get the run's git configuration: %q", env)
	}
	if !strings.HasPrefix(pwd, runs) {
		t.Errorf("the Warden ran in %q, not under its run directory", pwd)
	}
}

func TestWardenThatCannotBePreparedFailsTheStepBeforeSpawning(t *testing.T) {
	g := newWardenRig(t, "success.jsonl")
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g.r.UseDefinitions(reg)
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o600)
	g.r.UseSkills(SkillsDelivery{Catalogue: skills.Vendored(), RunDir: func(run string) string { return filepath.Join(blocker, run) }})
	g.startStep("a")
	sr := g.stepEnds("a")
	if sr.Status != store.StepFailed || !strings.Contains(sr.Reason, "preparing the Warden's directory") {
		t.Fatalf("step = %+v", sr)
	}
	if g.file("args") != "" {
		t.Error("the Warden spawned anyway")
	}
}

func TestRunningWardensAreShownInTheQueueAndCountedActive(t *testing.T) {
	g := newWardenRig(t, "hang.jsonl")
	t.Setenv("ARCHWRIGHT_STUB_HOLD", "1")
	g.startStep("a")
	eventually(t, "the stub to start", func() bool { return g.file("env") != "" })
	q := g.r.Queue()
	if len(q.Wardens) != 1 || q.Wardens[0].Step != "a" || q.Wardens[0].Guild != "masonry" || len(q.Running) != 0 || len(q.Queued) != 0 {
		t.Fatalf("queue = %+v", q)
	}
	if n, err := g.r.ActiveRuns(); err != nil || n != 1 {
		t.Fatalf("active runs = %d, %v", n, err)
	}
	b, _ := json.Marshal(q)
	if !strings.Contains(string(b), `"wardens":[{"job"`) {
		t.Errorf("queue json = %s", b)
	}
}

func TestReviewWardenRunsReadOnlyInTheReviewWorktreeAndReports(t *testing.T) {
	g := newWardenRig(t, "success.jsonl")
	guilds := func(name string) (Guild, error) {
		switch name {
		case "masonry":
			return agentGuilds["masonry"], nil
		case ReviewGuild:
			return Guild{Name: name, Binding: defs.Binding{Guild: name, Runner: "claude", Access: defs.ReadOnly}}, nil
		}
		return Guild{}, errInvalidInput
	}
	g.r.Configure(guilds, g.runner) // Wrights stay fake: the handback is not under test
	td, _ := filepath.Abs("../runner/testdata")
	env := map[string]string{EnvClaudeBinary: filepath.Join(td, "stub-claude.sh")}
	_, rv := WardenRunnersFrom(func(k string) string { return env[k] }, guilds, g.st)
	g.r.ConfigureReview(rv)
	g.ok(g.arch, "link_writ", `{"job_id":"`+g.job.ID+`","writ":"Ticket 8: add notes"}`, nil)
	g.commit("notes.md", "x")
	g.handBack("p")
	g.agentCalls("report_review", `{"standards":{"verdict":"pass"},"spec":{"verdict":"pass"}}`)
	g.ok(g.arch, "request_review", `{"job_id":"`+g.job.ID+`"}`, nil)
	var rev store.Review
	eventually(t, "the review to finish", func() bool {
		revs, _ := g.st.Reviews(g.job.ID)
		if len(revs) == 1 {
			rev = revs[0]
		}
		return rev.ID != "" && rev.Status != store.ReviewRunning
	})
	if args := g.file("args"); strings.Contains(args, "--permission-mode\nplan") || !strings.Contains(args, "--disallowed-tools\nEdit,Write") {
		t.Errorf("the review was not read-only: %q", args)
	}
	if rev.Status != store.ReviewReported {
		t.Fatalf("the stub's report_review should have passed the review: %+v", rev)
	}
	eventually(t, "the review run to be recorded as over", func() bool {
		recs, _ := g.st.RunRecords(g.job.ID)
		for _, r := range recs {
			if r.Guild == ReviewGuild && r.Runner == "claude" && r.Status == "done" {
				return true
			}
		}
		return false
	})
}

func TestWardenRunnersFromHonoursTheFakeSwitch(t *testing.T) {
	w, rv := WardenRunnersFrom(func(k string) string {
		if k == EnvWrightRunner {
			return "fake"
		}
		return ""
	}, nil, nil)
	if _, ok := w.(*FakeWarden); !ok || rv != nil {
		t.Fatalf("fake: %T %v", w, rv)
	}
	w, rv = WardenRunnersFrom(func(string) string { return "" }, nil, nil)
	if aw, ok := w.(*AgentWarden); !ok || rv != ReviewRunner(aw) {
		t.Fatalf("real is the default: %T %T", w, rv)
	}
}
