package works

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/launch"
	"github.com/phallatlassian/archwright/internal/runner"
	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// standingRig runs Plan-step Wardens and reviews through the real SessionRunner
// onto a fake Session runner, which plays each Warden's turn: it hands its step
// back through the spool, in the run whose variables the launch or the Nudge named.
type standingRig struct {
	*gateRig
	fake *hosted.Fake
	sr   *SessionRunner
	arch RunEnv
	n    int
}

// carriesOutItsStep is the agent of a Warden Session: it carries out the step
// it was given in the run its launch or Nudge names, as the fake Warden does,
// dispatching a Wright for it, accepting it and handing back.
func carriesOutItsStep(st *store.Store) func(context.Context, hosted.LaunchRequest) error {
	return func(ctx context.Context, req hosted.LaunchRequest) error {
		running, err := st.RunningSteps()
		if err != nil || len(running) == 0 {
			return fmt.Errorf("no step is running (%v)", err)
		}
		env := RunEnv{Run: req.Env[spool.EnvRun], Spool: req.Env[spool.EnvSpool], Token: req.Env[spool.EnvToken]}
		return NewFakeWarden().RunWarden(ctx, WardenRequest{Env: env, Step: running[0].Step})
	}
}

func newStandingRig(t *testing.T) *standingRig {
	t.Helper()
	g := newGateRig(t, store.Gates{})
	script := hosted.Script{Steps: hosted.Turn(carriesOutItsStep(g.st)).Steps, Nudged: []hosted.Step{
		{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}},
		{Act: carriesOutItsStep(g.st)},
		{Hook: &hosted.Hook{Event: hosted.HookStop}},
	}}
	fake := hosted.NewFake(script)
	guilds := func(name string) (Guild, error) {
		if name == ReviewGuild {
			return Guild{Name: name, Binding: defs.Binding{Guild: name, Runner: "claude", Access: defs.ReadOnly}}, nil
		}
		if gd, ok := agentGuilds[name]; ok {
			return gd, nil
		}
		return Guild{}, errInvalidInput
	}
	sr := &SessionRunner{Runners: map[string]hosted.Runner{"claude": fake}, Guilds: guilds}
	g.r.Configure(guilds, NewFakeRunner(nil)) // Wrights are not the subject here
	g.r.ConfigureWarden(sr)
	g.r.ConfigureReview(sr)
	g.r.UseSessions(sr, time.Minute)
	return &standingRig{gateRig: g, fake: fake, sr: sr, arch: startRun(t, g.r, RoleArchwright)}
}

// planFor gives a Job an approved Plan of independent steps, each "id:guild".
func (g *standingRig) planFor(job string, steps ...string) {
	g.t.Helper()
	var plan []store.Step
	for _, s := range steps {
		id, guild, _ := strings.Cut(s, ":")
		plan = append(plan, store.Step{ID: id, Guild: guild, Title: "Do " + id})
	}
	if _, err := g.st.SetPlan(job, plan); err != nil {
		g.t.Fatal(err)
	}
	if _, err := g.st.DecidePlan(job, true, time.Time{}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *standingRig) startStep(job, step string) {
	g.t.Helper()
	g.n++
	if resp := call(g.t, g.arch, "s"+string(rune('a'+g.n)), "start_step", `{"job":"`+job+`","step":"`+step+`"}`); !resp.OK {
		g.t.Fatalf("start_step %s: %+v", step, resp.Error)
	}
}

func (g *standingRig) handedBack(job, step string) store.StepRun {
	g.t.Helper()
	var sr store.StepRun
	eventually(g.t, "step "+step+" to end", func() bool {
		sr, _ = g.st.GetStepRun(job, step)
		return sr.Status != store.StepRunning
	})
	if sr.Status != store.StepHandedBack {
		g.t.Fatalf("step %s = %+v", step, sr)
	}
	return sr
}

func TestWardenSessionIsReusedAcrossTheStepsOfItsGuild(t *testing.T) {
	g := newStandingRig(t)
	g.planFor(g.job.ID, "one:masonry", "two:masonry")
	g.startStep(g.job.ID, "one")
	g.handedBack(g.job.ID, "one")
	g.startStep(g.job.ID, "two")
	second := g.handedBack(g.job.ID, "two")

	if n := len(g.fake.Launches()); n != 1 {
		t.Fatalf("%d Sessions launched for one Guild's two steps", n)
	}
	launched := g.fake.Launches()[0]
	if launched.Role != RoleWarden || !strings.Contains(launched.Brief, "Do one") {
		t.Errorf("launch = %+v", launched)
	}
	nudges := g.fake.Nudges()
	if len(nudges) != 1 || !strings.Contains(nudges[0], "Do two") || !strings.Contains(nudges[0], "standing Warden") {
		t.Fatalf("the second step was not Nudged into the Session: %q", nudges)
	}
	// The second step's run is the one the Nudge names: the Session handed it back through that run's spool.
	if !strings.Contains(nudges[0], "export ARCHWRIGHT_RUN=run-") || second.HandbackID == 0 {
		t.Errorf("nudge %q, step %+v", nudges[0], second)
	}
	eventually(t, "the Warden's Session to be idle after its steps", func() bool { return g.fake.Sessions()[0].State() == hosted.StateIdle })
}

func TestWardensOfDifferentGuildsGetSessionsOfTheirOwn(t *testing.T) {
	g := newStandingRig(t)
	g.planFor(g.job.ID, "build:masonry", "look:surveyors")
	g.startStep(g.job.ID, "build")
	g.handedBack(g.job.ID, "build")
	g.startStep(g.job.ID, "look")
	g.handedBack(g.job.ID, "look")
	if n := len(g.fake.Launches()); n != 2 || len(g.fake.Nudges()) != 0 {
		t.Fatalf("%d launches, nudges %q", n, g.fake.Nudges())
	}
}

func TestTwoJobsGetIndependentWardens(t *testing.T) {
	g := newStandingRig(t)
	other, err := g.st.CreateJobOn("demo", "Other", "")
	if err != nil {
		t.Fatal(err)
	}
	g.planFor(g.job.ID, "one:masonry")
	g.planFor(other.ID, "one:masonry")
	g.startStep(g.job.ID, "one")
	g.handedBack(g.job.ID, "one")
	g.startStep(other.ID, "one")
	g.handedBack(other.ID, "one")
	if n := len(g.fake.Launches()); n != 2 || len(g.fake.Nudges()) != 0 {
		t.Fatalf("%d launches, nudges %q: the second Job must not use the first Job's Warden", n, g.fake.Nudges())
	}
	if a, b := g.fake.Launches()[0].Job, g.fake.Launches()[1].Job; a == b {
		t.Errorf("both Sessions are for Job %s", a)
	}
}

func TestSessionsStayOpenUntilTheJobLeavesActiveWorkThenClose(t *testing.T) {
	g := newStandingRig(t)
	g.planFor(g.job.ID, "one:masonry", "look:surveyors")
	g.startStep(g.job.ID, "one")
	g.handedBack(g.job.ID, "one")
	g.startStep(g.job.ID, "look")
	g.handedBack(g.job.ID, "look")
	sessions := g.fake.Sessions()
	if len(sessions) != 2 {
		t.Fatalf("%d Sessions", len(sessions))
	}

	for _, s := range sessions {
		s := s
		eventually(t, "the Session to finish its turn", func() bool { return s.State() == hosted.StateIdle })
	}
	left := map[string]time.Time{}
	now := time.Now()
	g.r.reapSessions(left, now.Add(time.Hour)) // handed back, but the Job is still in active work
	for i, s := range sessions {
		if s.State() != hosted.StateIdle {
			t.Fatalf("Session %d is %s after handback, with its Job still active", i, s.State())
		}
	}

	if err := g.st.RecordPR(g.job.ID, "https://github.com/o/r/pull/1"); err != nil { // the PR is opened: the Job leaves active work
		t.Fatal(err)
	}
	g.r.reapSessions(left, now)
	for i, s := range sessions {
		if s.State() != hosted.StateIdle {
			t.Fatalf("Session %d is %s before the retention has passed", i, s.State())
		}
	}
	g.r.reapSessions(left, now.Add(2*time.Minute))
	for i, s := range sessions {
		if st := s.Result().Status; st != hosted.StatusCancelled {
			t.Errorf("Session %d ended %s, want it closed", i, st)
		}
	}
	if jobs := g.sr.OpenJobs(); len(jobs) != 0 {
		t.Errorf("still holds Sessions for %v", jobs)
	}
}

func TestACloseLeavesASessionThatIsNotIdleAlone(t *testing.T) {
	g := newStandingRig(t)
	g.fake.SetScript(hosted.Script{Steps: []hosted.Step{
		{Hook: &hosted.Hook{Event: hosted.HookSessionStart}},
		{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}},
		{Hook: &hosted.Hook{Event: hosted.HookPermissionReq, ToolName: "Bash"}},
	}})
	g.planFor(g.job.ID, "one:masonry")
	g.startStep(g.job.ID, "one")
	eventually(t, "the Warden to wait on a permission", func() bool {
		ss := g.fake.Sessions()
		return len(ss) == 1 && ss[0].State() == hosted.StateWaitingOnPermission
	})
	if n := g.sr.CloseJob(g.job.ID); n != 1 {
		t.Fatalf("CloseJob left %d Sessions, want the one at a prompt", n)
	}
	if s := g.fake.Sessions()[0]; s.State() != hosted.StateWaitingOnPermission {
		t.Fatalf("a Session at a prompt was touched: %s", s.State())
	}
}

// A review is a read-only Session of the Review Guild's own Warden: it never
// lands in a builder's Session, and it carries nothing of how the code was built.
func TestReviewRunsAsAReadOnlySessionSharingNoContextWithTheBuilder(t *testing.T) {
	g := newStandingRig(t)
	g.planFor(g.job.ID, "build:masonry")
	g.startStep(g.job.ID, "build")
	g.handedBack(g.job.ID, "build")
	g.fake.SetScript(nudgedScript()) // the Review Warden reads and reports; it carries out no step

	review := func(id, wt string) {
		env, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: ReviewGuild, Review: id})
		if err != nil {
			t.Fatal(err)
		}
		if err := g.sr.RunReview(context.Background(), ReviewRequest{Env: env, Job: g.job.ID, ReviewID: id, Worktree: wt, Brief: "# Review " + id + "\n\nLook at " + wt}); err != nil {
			t.Fatal(err)
		}
	}
	wt := t.TempDir()
	review("r1", wt)
	launches := g.fake.Launches()
	if len(launches) != 2 {
		t.Fatalf("%d launches: the review must be a Session of its own", len(launches))
	}
	b, rv := launches[0], launches[1]
	if rv.Spawn.EffectiveMode() != "plan" {
		t.Errorf("the review runs in %s mode, want read-only plan mode", rv.Spawn.EffectiveMode())
	}
	if strings.Contains(rv.Brief, "Do build") || rv.Dir == b.Dir || rv.Env[spool.EnvRun] == b.Env[spool.EnvRun] {
		t.Errorf("the review shares something with the builder's Warden: %+v / %+v", rv, b)
	}
	if g.fake.Sessions()[0] == g.fake.Sessions()[1] {
		t.Fatal("the review was run in the builder's Session")
	}

	// Its second pass is Nudged into its own Session, not a builder's.
	review("r2", wt)
	if n := len(g.fake.Launches()); n != 2 {
		t.Fatalf("%d launches after a second review, want the Review Warden reused", n)
	}
	nudges := g.fake.Nudges()
	if len(nudges) != 1 || !strings.Contains(nudges[0], "# Review r2") || !strings.Contains(nudges[0], "further review") {
		t.Fatalf("nudges = %q", nudges)
	}

	// A review of another checkout gets a Session in that checkout.
	wt2 := t.TempDir()
	review("r3", wt2)
	launches = g.fake.Launches()
	if len(launches) != 3 || launches[2].Dir != wt2 {
		t.Fatalf("a review in another checkout was not launched there: %+v", launches)
	}
	if n := len(g.fake.Nudges()); n != 1 {
		t.Fatalf("%d nudges, want the review in another checkout launched, not Nudged", n)
	}
}

func TestWardenSessionOfAGuildBoundToNoSessionRunnerFailsItsStep(t *testing.T) {
	g := newStandingRig(t)
	g.planFor(g.job.ID, "carve:carvers") // codex-bound; this rig has no codex Session runner
	g.startStep(g.job.ID, "carve")
	var sr store.StepRun
	eventually(t, "the step to fail", func() bool {
		sr, _ = g.st.GetStepRun(g.job.ID, "carve")
		return sr.Status == store.StepFailed
	})
	if !strings.Contains(sr.Reason, "cannot run as a Session") {
		t.Errorf("reason = %q", sr.Reason)
	}
}

func TestRunningWardensAreShownInTheQueueAndCountedActive(t *testing.T) {
	g := newStandingRig(t)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	g.fake.SetScript(hosted.Turn(func(ctx context.Context, _ hosted.LaunchRequest) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}))
	g.planFor(g.job.ID, "a:masonry")
	g.startStep(g.job.ID, "a")
	eventually(t, "the Warden's Session to launch", func() bool { return len(g.fake.Launches()) == 1 })
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

// ---- the Archwright as a Session the Works owns ----

type archRig struct {
	*rig
	fake *hosted.Fake
	home string
}

func newArchRig(t *testing.T, script hosted.Script) *archRig {
	t.Helper()
	r, st := newRuns(t)
	fake := hosted.NewFake(script)
	home := filepath.Join(t.TempDir(), "home")
	r.UseArchwright(&ArchwrightHost{
		Runners:   map[string]hosted.Runner{"claude": fake},
		Choice:    func() (string, error) { return "claude", nil },
		Home:      home,
		SpoolRoot: func(string) string { return t.TempDir() },
	})
	return &archRig{rig: &rig{t: t, r: r, st: st}, fake: fake, home: home}
}

func TestWorksSpawnsAndOwnsTheArchwrightSessionInTheArchwrightWindow(t *testing.T) {
	g := newArchRig(t, hosted.Turn(nil))
	info, err := g.r.StartArchwright("shop")
	if err != nil || !info.Started || info.Pane == "" || info.Runner != "claude" {
		t.Fatalf("StartArchwright = %+v, %v", info, err)
	}
	req := g.fake.Launches()[0]
	if req.Role != RoleArchwright || req.Dir != g.home || req.Job != "" {
		t.Errorf("launch = %+v", req)
	}
	if !strings.Contains(req.Brief, "You are the Archwright") || !strings.Contains(req.Brief, `"shop"`) || !strings.Contains(req.Brief, "nudges you") || strings.Contains(req.Brief, launch.HooksMarker) {
		t.Errorf("brief = %q", req.Brief)
	}
	for _, k := range []string{spool.EnvRun, spool.EnvSpool, spool.EnvToken, launch.EnvSite} {
		if req.Env[k] == "" {
			t.Errorf("env lacks %s", k)
		}
	}
	if req.Spawn.EffectiveMode() != runner.ModeDefault {
		t.Errorf("mode = %s", req.Spawn.EffectiveMode())
	}
	// It reaches the Works through the spool the Works made for it.
	env := RunEnv{Run: req.Env[spool.EnvRun], Role: RoleArchwright, Spool: req.Env[spool.EnvSpool], Token: req.Env[spool.EnvToken]}
	if resp := call(t, env, "l1", "list_jobs", ""); !resp.OK {
		t.Fatalf("list_jobs: %+v", resp.Error)
	}
	// Asking again finds the one that is open.
	again, err := g.r.StartArchwright("")
	if err != nil || again.Started || again.Pane != info.Pane || len(g.fake.Launches()) != 1 {
		t.Fatalf("second StartArchwright = %+v, %v (%d launches)", again, err, len(g.fake.Launches()))
	}
}

func TestWorksThatHostsNoArchwrightSaysSo(t *testing.T) {
	r, _ := newRuns(t)
	if _, err := r.StartArchwright(""); err == nil || err.Error() != MsgArchwrightNotHosted {
		t.Fatalf("err = %v", err)
	}
}

func fileItem(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.QueueCommand("instruct", "", "SECRET words", nil); err != nil {
		t.Fatal(err)
	}
}

func nudgedScript() hosted.Script {
	s := hosted.Turn(nil)
	s.Nudged = []hosted.Step{{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}}, {Hook: &hosted.Hook{Event: hosted.HookStop}}}
	return s
}

func TestAnInboxItemNudgesTheIdleArchwrightOncePerItemAndWithNoContent(t *testing.T) {
	g := newArchRig(t, nudgedScript())
	if _, err := g.r.StartArchwright(""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Archwright to finish its first turn", func() bool { return g.fake.Sessions()[0].Info().Turns == 1 })
	fileItem(t, g.st)
	eventually(t, "the Nudge", func() bool { return len(g.fake.Nudges()) == 1 })
	if got := g.fake.Nudges()[0]; got != launch.Nudge || strings.Contains(got, "SECRET") {
		t.Fatalf("nudge = %q", got)
	}
	// It went idle again without reading: the same item is not nudged twice.
	eventually(t, "the Archwright to be idle again", func() bool { s := g.fake.Sessions()[0]; return s.Info().Turns == 2 && s.State() == hosted.StateIdle })
	time.Sleep(150 * time.Millisecond)
	if n := len(g.fake.Nudges()); n != 1 {
		t.Fatalf("%d nudges for one item", n)
	}
	fileItem(t, g.st) // something new
	eventually(t, "the second Nudge", func() bool { return len(g.fake.Nudges()) == 2 })
}

func TestAnItemFiledWhileTheArchwrightIsBusyIsNudgedWhenItGoesIdle(t *testing.T) {
	release := make(chan struct{})
	sc := nudgedScript()
	sc.Steps = []hosted.Step{
		{Hook: &hosted.Hook{Event: hosted.HookSessionStart}},
		{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}},
		{Act: func(ctx context.Context, _ hosted.LaunchRequest) error {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		}},
		{Hook: &hosted.Hook{Event: hosted.HookStop}},
	}
	g := newArchRig(t, sc)
	if _, err := g.r.StartArchwright(""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Archwright to be working", func() bool { return g.fake.Sessions()[0].State() == hosted.StateWorking })
	fileItem(t, g.st)
	time.Sleep(150 * time.Millisecond)
	if n := len(g.fake.Nudges()); n != 0 {
		t.Fatalf("a working Archwright was nudged (%d)", n)
	}
	close(release)
	eventually(t, "the Nudge once idle", func() bool { return len(g.fake.Nudges()) == 1 })
}

func TestArchwrightSessionEndsWithItsRunAndAnotherCanBeSpawned(t *testing.T) {
	g := newArchRig(t, hosted.Script{Steps: hosted.Turn(nil).Steps, End: true})
	first, err := g.r.StartArchwright("")
	if err != nil {
		t.Fatal(err)
	}
	g.fake.Sessions()[0].Result()
	var second ArchwrightInfo
	eventually(t, "a new Archwright to be spawned", func() bool {
		second, err = g.r.StartArchwright("")
		return err == nil && second.Started
	})
	if second.Run == first.Run || len(g.fake.Launches()) != 2 {
		t.Fatalf("first %+v second %+v (%d launches)", first, second, len(g.fake.Launches()))
	}
}

func TestClosingTheWorksCancelsTheArchwrightSession(t *testing.T) {
	g := newArchRig(t, hosted.Turn(nil))
	if _, err := g.r.StartArchwright(""); err != nil {
		t.Fatal(err)
	}
	g.r.Close()
	if st := g.fake.Sessions()[0].Result().Status; st != hosted.StatusCancelled {
		t.Fatalf("status = %s", st)
	}
	if _, err := os.Stat(g.home); err != nil {
		t.Fatal(err)
	}
}

// A Wright's Session is a Session of its Job too: it stays open after it
// reports, and goes when the Job leaves active work.
func TestWrightSessionIsClosedWhenItsJobLeavesActiveWork(t *testing.T) {
	g := newSessionRig(t, hosted.Turn(callsTool("report_result", `{"status":"done","result":"branch x","evidence":["go test"]}`)))
	sr := g.r.runner.(*SessionRunner)
	g.r.UseSessions(sr, time.Minute)
	g.send("p")
	g.waitReported("p")
	s := g.fake.Sessions()[0]
	eventually(t, "the Session to finish its turn", func() bool { return s.State() == hosted.StateIdle })
	left, now := map[string]time.Time{}, time.Now()
	g.r.reapSessions(left, now.Add(time.Hour))
	if s.State() != hosted.StateIdle {
		t.Fatalf("the Wright's Session is %s while its Job is active", s.State())
	}
	if err := g.st.CloseJob(g.job.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
	g.r.reapSessions(left, now)
	g.r.reapSessions(left, now.Add(2*time.Minute))
	if st := s.Result().Status; st != hosted.StatusCancelled {
		t.Fatalf("the Wright's Session ended %s, want it closed", st)
	}
}

func TestArchwrightEndpointSpawnsItOrSaysThisWorksHostsNone(t *testing.T) {
	g := newArchRig(t, hosted.Turn(nil))
	srv := httptest.NewServer(newHandler(g.st, g.r, nil, testToken, 1, func() {}, nil))
	defer srv.Close()
	d := &dash{rig: g.rig, srv: srv}
	code, body := d.do("POST", "/archwright", `{"Site":"shop"}`, nil)
	var info ArchwrightInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil || code != 200 || !info.Started || info.Pane == "" {
		t.Fatalf("POST /archwright = %d %s", code, body)
	}
	if code, body := d.do("POST", "/archwright", `{}`, nil); code != 200 || !strings.Contains(body, `"started":false`) {
		t.Fatalf("the open one is reported, not respawned: %d %s", code, body)
	}

	bare, _ := newRuns(t)
	srv2 := httptest.NewServer(newHandler(g.st, bare, nil, testToken, 1, func() {}, nil))
	defer srv2.Close()
	code, body = (&dash{rig: g.rig, srv: srv2}).do("POST", "/archwright", `{}`, nil)
	if code != http.StatusNotImplemented || !strings.Contains(body, MsgArchwrightNotHosted) {
		t.Fatalf("a Works with no Sessions: %d %s", code, body)
	}
}

// Two independent steps of one Guild, started together, are carried out one
// after the other in the one Warden Session: no second Session is launched
// beside it, and the first is never cancelled.
func TestStepsOfOneGuildStartedTogetherAreSerialisedInOneWardenSession(t *testing.T) {
	g := newStandingRig(t)
	g.planFor(g.job.ID, "one:masonry", "two:masonry", "three:masonry")
	g.startStep(g.job.ID, "one")
	g.startStep(g.job.ID, "two")
	g.startStep(g.job.ID, "three")
	for _, id := range []string{"one", "two", "three"} {
		g.handedBack(g.job.ID, id)
	}
	if n, nu := len(g.fake.Launches()), len(g.fake.Nudges()); n != 1 || nu != 2 {
		t.Fatalf("%d launches and %d nudges for three steps of one Guild", n, nu)
	}
	if st := g.fake.Sessions()[0].State(); st == hosted.StateEnded {
		t.Fatalf("the Warden's Session was ended")
	}
}

func TestTheRoleOfTheArchwrightIsTheSameInTheWorksAndTheSessionAdapter(t *testing.T) {
	if RoleArchwright != hosted.RoleArchwright {
		t.Fatalf("works says %q, the tmux adapter %q", RoleArchwright, hosted.RoleArchwright)
	}
}

// The Job's standing Warden is adopted after a restart like a Wright: its next
// step is Nudged into the same Session, not run in a second one.
func TestRestartAdoptsTheStandingWardenAndTheNextStepIsNudgedIntoIt(t *testing.T) {
	g := newStandingRig(t)
	g.planFor(g.job.ID, "one:masonry", "two:masonry")
	g.startStep(g.job.ID, "one")
	g.handedBack(g.job.ID, "one")
	eventually(t, "the Warden's Session to be idle", func() bool { return g.fake.Sessions()[0].State() == hosted.StateIdle })
	// This rig records nothing about Sessions: record what a Works would have.
	recs, _ := g.st.RunRecords(g.job.ID)
	info := g.fake.Sessions()[0].Info()
	g.st.SetRunSession(recs[0].RunID, sessionRecord(info, hosted.StateIdle))

	g.r.Close()
	if _, err := g.st.InterruptDispatches(); err != nil {
		t.Fatal(err)
	}
	r2 := NewRuns(g.st)
	r2.poll = 5 * time.Millisecond
	t.Cleanup(r2.Close)
	r2.UseSites(g.sm)
	sr := &SessionRunner{Runners: map[string]hosted.Runner{"claude": g.fake}, Guilds: g.sr.Guilds}
	r2.Configure(g.sr.Guilds, NewFakeRunner(nil))
	r2.ConfigureWarden(sr)
	r2.UseSessions(sr, time.Minute)
	rec, err := r2.RecoverSessions()
	if err != nil || rec.Adopted != 1 {
		t.Fatalf("recovery = %+v, %v", rec, err)
	}
	g.r, g.arch = r2, startRun(t, r2, RoleArchwright)
	g.startStep(g.job.ID, "two")
	g.handedBack(g.job.ID, "two")
	if n := len(g.fake.Launches()); n != 1 {
		t.Fatalf("%d Sessions launched: the Warden was not adopted", n)
	}
	if nudges := g.fake.Nudges(); len(nudges) != 1 || !strings.Contains(nudges[0], "Do two") {
		t.Fatalf("nudges = %q", nudges)
	}
}
