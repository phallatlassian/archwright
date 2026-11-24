package works

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/runner"
	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// sessionRig dispatches Wrights through the real SessionRunner onto a fake
// Session runner that plays hooks and calls the Works' tools through the spool
// as a Claude Session would.
type sessionRig struct {
	*gateRig
	fake *hosted.Fake
}

func newSessionRig(t *testing.T, script hosted.Script) *sessionRig {
	t.Helper()
	g := &sessionRig{gateRig: newGateRig(t, store.Gates{}), fake: hosted.NewFake(script)}
	guilds := func(name string) (Guild, error) {
		if gd, ok := agentGuilds[name]; ok {
			return gd, nil
		}
		return Guild{}, errInvalidInput
	}
	sr := &SessionRunner{
		Runners:  map[string]hosted.Runner{"claude": g.fake},
		Guilds:   guilds,
		Fallback: NewFakeRunner(nil),
		Record: func(run string, info hosted.Info, state hosted.State) {
			g.st.SetRunSession(run, info.Pane, info.SessionID, info.TranscriptPath, string(state))
		},
	}
	g.r.Configure(guilds, sr)
	return g
}

// callsTool is a Session script step: the agent calls a tool through the spool.
func callsTool(tool, input string) func(context.Context, hosted.LaunchRequest) error {
	return func(ctx context.Context, req hosted.LaunchRequest) error {
		env, err := spool.EnvFrom(func(k string) string { return req.Env[k] })
		if err != nil {
			return err
		}
		resp, err := spool.Call(ctx, env, "tool-1", tool, json.RawMessage(input), spool.DefaultWait)
		if err != nil {
			return err
		}
		if !resp.OK {
			return errors.New(tool + ": " + resp.Error.Message)
		}
		return nil
	}
}

func (g *sessionRig) send(piece string) {
	g.t.Helper()
	g.ok(g.warden, "dispatch_wright", `{"piece":"`+piece+`","brief":`+goodBrief+`}`, nil)
}

func (g *sessionRig) timeline(run string) (kinds, summaries []string) {
	g.t.Helper()
	evs, err := g.st.RunEvents(run)
	if err != nil {
		g.t.Fatal(err)
	}
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
		summaries = append(summaries, e.Summary)
	}
	return kinds, summaries
}

func (g *sessionRig) ended(run string) store.RunRecord {
	g.t.Helper()
	var rec store.RunRecord
	eventually(g.t, "the run to end", func() bool { rec, _ = g.st.GetRunRecord(run); return rec.Status != "running" })
	return rec
}

func TestWrightRunsAsASessionReportsThroughTheSpoolAndItsStatesShowOnTheTimeline(t *testing.T) {
	g := newSessionRig(t, hosted.Turn(callsTool("report_result", `{"status":"done","result":"branch x","evidence":["go test"]}`)))
	g.send("p")
	d := g.waitReported("p")
	if d.Status != store.DispatchReported || !strings.Contains(string(d.Result), `"done"`) || !strings.Contains(string(d.Result), "branch x") {
		t.Fatalf("dispatch = %s %s", d.Status, d.Result)
	}

	// It was launched into a worktree of its own, as a Wright, with the brief and its run's spool.
	launches := g.fake.Launches()
	if len(launches) != 1 {
		t.Fatalf("%d launches", len(launches))
	}
	req := launches[0]
	wts, _ := g.st.Worktrees(g.job.ID)
	inWright := false
	for _, w := range wts {
		if w.Kind == store.KindWright && w.Path == req.Dir {
			inWright = true
		}
	}
	if !inWright || req.Role != RoleWright || req.Job != g.job.ID || !strings.Contains(req.Brief, "Add login") {
		t.Errorf("launch = %+v, worktrees %+v", req, wts)
	}
	for _, k := range []string{spool.EnvRun, spool.EnvSpool, spool.EnvToken} {
		if req.Env[k] == "" {
			t.Errorf("env lacks %s", k)
		}
	}
	if req.Spawn.EffectiveMode() != runner.ModeDefault {
		t.Errorf("mode = %s", req.Spawn.EffectiveMode())
	}

	// The Works saw its state transitions: from the hooks, on the run's timeline.
	rec := g.ended(d.RunID)
	kinds, summaries := g.timeline(d.RunID)
	var states []string
	for i, k := range kinds {
		if k == "session" {
			states = append(states, summaries[i])
		}
	}
	if kinds[0] != "posture" || !slices.Equal(states, []string{"idle", "working", "idle"}) {
		t.Fatalf("timeline = %v %v", kinds, summaries)
	}
	for _, e := range mustEvents(t, g.st, d.RunID) {
		if !e.HasRaw {
			t.Errorf("event %d (%s) lost its raw hook payload", e.Seq, e.Kind)
		}
	}
	// Its pane, vendor session and state are in the run's record.
	if rec.Status != "done" || rec.PaneID == "" || rec.SessionID != "fake-session" || rec.TranscriptPath != "/fake/transcript.jsonl" || rec.SessionState != "idle" {
		t.Errorf("run record = %+v", rec)
	}
	// The Wright's Session stays open after it reported: it was idle, not ended or cancelled.
	if s := g.fake.Sessions()[0]; s.State() != hosted.StateIdle {
		t.Errorf("session state after the dispatch = %s", s.State())
	}
}

func mustEvents(t *testing.T, st *store.Store, run string) []store.RunEvent {
	t.Helper()
	evs, err := st.RunEvents(run)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestSessionThatFinishesItsTurnWithoutReportingFailsTheDispatch(t *testing.T) {
	g := newSessionRig(t, hosted.Turn(nil))
	g.send("p")
	d := g.waitReported("p")
	if !strings.Contains(string(d.Result), `"failed"`) || !strings.Contains(string(d.Result), "ended without reporting") {
		t.Fatalf("result = %s", d.Result)
	}
}

func TestSessionThatEndsAtOnceFailsTheDispatchWithItsError(t *testing.T) {
	g := newSessionRig(t, hosted.Script{Steps: []hosted.Step{
		{Hook: &hosted.Hook{Event: hosted.HookSessionStart}},
		{Act: func(context.Context, hosted.LaunchRequest) error { return errors.New("exited with status 3") }},
	}})
	g.send("p")
	d := g.waitReported("p")
	if !strings.Contains(string(d.Result), "the run failed") || !strings.Contains(string(d.Result), "exited with status 3") {
		t.Fatalf("result = %s", d.Result)
	}
	if rec := g.ended(d.RunID); rec.Status != "failed" {
		t.Errorf("run record = %+v", rec)
	}
}

func TestSessionThatNeverStartsFailsTheDispatchAsFailedToStart(t *testing.T) {
	g := newSessionRig(t, hosted.Script{NoStart: true})
	g.send("p")
	d := g.waitReported("p")
	if !strings.Contains(string(d.Result), "failed to start") {
		t.Fatalf("result = %s", d.Result)
	}
}

func TestPermissionWaitShowsOnTheTimelineAndCancellingTheDispatchCancelsTheSession(t *testing.T) {
	g := newSessionRig(t, hosted.Script{Steps: []hosted.Step{
		{Hook: &hosted.Hook{Event: hosted.HookSessionStart, SessionID: "s1"}},
		{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}},
		{Hook: &hosted.Hook{Event: hosted.HookPermissionReq, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"rm -rf build"}`)}},
	}})
	g.send("p")
	var run string
	eventually(g.t, "the Wright to wait on a permission", func() bool {
		d, err := g.st.LatestDispatch(g.job.ID, "p")
		if err != nil || d.RunID == "" {
			return false
		}
		run = d.RunID
		rec, _ := g.st.GetRunRecord(run)
		return rec.SessionState == string(hosted.StateWaitingOnPermission)
	})
	_, summaries := g.timeline(run)
	if !slices.Contains(summaries, "waiting on permission: Bash: rm -rf build") {
		t.Fatalf("timeline = %v", summaries)
	}
	if d, _ := g.st.LatestDispatch(g.job.ID, "p"); d.Status != store.DispatchRunning {
		t.Fatalf("a Wright waiting at a prompt is not done: %s", d.Status)
	}
	if _, err := g.r.CancelDispatch(mustDispatch(t, g, "p").ID); err != nil {
		t.Fatal(err)
	}
	d := g.waitReported("p")
	if !strings.Contains(string(d.Result), cancelledByDeveloper) {
		t.Fatalf("result = %s", d.Result)
	}
	if s := g.fake.Sessions()[0]; s.Result().Status != hosted.StatusCancelled {
		t.Fatalf("the Session was not cancelled: %+v", s.Result())
	}
}

func mustDispatch(t *testing.T, g *sessionRig, piece string) store.Dispatch {
	t.Helper()
	d, err := g.st.LatestDispatch(g.job.ID, piece)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestReadOnlyGuildSessionsLaunchInPlanMode(t *testing.T) {
	g := newSessionRig(t, hosted.Turn(callsTool("report_result", `{"status":"done","result":"r","evidence":["e"]}`)))
	w, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: "surveyors"})
	if err != nil {
		t.Fatal(err)
	}
	g.ok(w, "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`, nil)
	g.waitReported("p")
	if got := g.fake.Launches()[0].Spawn.Mode; got != "plan" {
		t.Fatalf("mode = %q", got)
	}
}

func TestGuildBoundToARunnerWithNoSessionAdapterRunsOnTheFallback(t *testing.T) {
	g := newSessionRig(t, hosted.Script{})
	w, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: "carvers"}) // codex-bound
	if err != nil {
		t.Fatal(err)
	}
	g.ok(w, "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`, nil)
	d := g.waitReported("p")
	if d.Status != store.DispatchReported || !strings.Contains(string(d.Result), "fake://p") {
		t.Fatalf("dispatch = %s %s", d.Status, d.Result)
	}
	if n := len(g.fake.Launches()); n != 0 {
		t.Fatalf("%d Sessions launched for a codex Guild", n)
	}
}

// refusingRunner is a Session runner whose never-list refuses every launch.
type refusingRunner struct{ hosted.Fake }

func (r *refusingRunner) Launch(context.Context, hosted.LaunchRequest) (hosted.Session, error) {
	return nil, errors.New("forbidden by the never-list: permission mode \"bypassPermissions\" is more permissive than the runner's default")
}

func TestRefusedLaunchFailsTheDispatchClosedAndSpawnsNothing(t *testing.T) {
	g := newSessionRig(t, hosted.Script{})
	g.r.runner.(*SessionRunner).Runners["claude"] = &refusingRunner{}
	g.send("p")
	d := g.waitReported("p")
	if !strings.Contains(string(d.Result), "was not started") || !strings.Contains(string(d.Result), "never-list") {
		t.Fatalf("result = %s", d.Result)
	}
	if n := len(g.fake.Launches()); n != 0 {
		t.Fatalf("%d launches", n)
	}
}

func TestTmuxWrightRunnerIsOptInAndNeverFallsBackSilently(t *testing.T) {
	guilds := func(string) (Guild, error) { return Guild{}, errInvalidInput }
	env := map[string]string{EnvWrightRunner: "tmux", "HOME": t.TempDir()}
	got := WrightRunnerFrom(func(k string) string { return env[k] }, guilds, nil)
	sr, ok := got.(*SessionRunner)
	if !ok || sr.Runners["claude"] == nil || sr.Fallback == nil {
		t.Fatalf("runner = %T", got)
	}
	// With no way to place its files, the tmux runner fails the run rather than running headless.
	got = WrightRunnerFrom(func(k string) string { return map[string]string{EnvWrightRunner: "tmux"}[k] }, guilds, nil)
	if err := got.Run(context.Background(), RunRequest{}); err == nil || !strings.Contains(err.Error(), "tmux Sessions") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := WrightRunnerFrom(func(string) string { return "" }, guilds, nil).(*AgentRunner); !ok {
		t.Fatal("the default is no longer the headless runner")
	}
}
