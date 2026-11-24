package works

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/runner"
	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/skills"
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

func TestCodexGuildRunsAsACodexSession(t *testing.T) {
	g := newSessionRig(t, hosted.Script{})
	codex := hosted.NewFakeFor("codex", hosted.Turn(callsTool("report_result", `{"status":"done","result":"r","evidence":["e"]}`)))
	g.r.runner.(*SessionRunner).Runners["codex"] = codex
	w, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: "carvers"}) // codex-bound
	if err != nil {
		t.Fatal(err)
	}
	g.ok(w, "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`, nil)
	d := g.waitReported("p")
	if d.Status != store.DispatchReported || !strings.Contains(string(d.Result), `"r"`) {
		t.Fatalf("dispatch = %s %s", d.Status, d.Result)
	}
	if n, c := len(codex.Launches()), len(g.fake.Launches()); n != 1 || c != 0 {
		t.Fatalf("%d codex and %d claude Sessions launched", n, c)
	}
	kinds, summaries := g.timeline(d.RunID)
	if len(summaries) == 0 || !strings.HasPrefix(strings.Join(summaries, "\n"), "codex fake:") {
		t.Fatalf("timeline = %v %v", kinds, summaries)
	}
	var rec store.RunRecord
	eventually(t, "the Codex Session to be recorded idle", func() bool {
		rec, _ = g.st.GetRunRecord(d.RunID)
		return rec.SessionState == "idle"
	})
	if rec.SessionID != "fake-session" || rec.PaneID == "" {
		t.Fatalf("run record = %+v", rec)
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
	if !ok || sr.Runners["claude"] == nil || sr.Runners["codex"] == nil || sr.Runners["codex"].Name() != "codex" || sr.Fallback == nil {
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

// reworkScript is a Wright Session that reports "first", and takes a Nudge as a
// new turn that reports "second" through the run the Nudge names.
func reworkScript() hosted.Script {
	s := hosted.Turn(callsTool("report_result", `{"status":"done","result":"first","evidence":["go test"]}`))
	s.Nudged = []hosted.Step{
		{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}},
		{Act: callsTool("report_result", `{"status":"done","result":"second","evidence":["go test"]}`)},
		{Hook: &hosted.Hook{Event: hosted.HookStop}},
	}
	return s
}

func TestReworkNudgesTheSameWrightSessionWithTheAmendedBrief(t *testing.T) {
	g := newSessionRig(t, reworkScript())
	g.send("p")
	first := g.waitReported("p")
	if !strings.Contains(string(first.Result), "first") {
		t.Fatalf("first result = %s", first.Result)
	}
	sess := g.fake.Sessions()[0]
	waitSessionIdle(t, sess)

	g.rework("p")
	second := g.waitReported("p")
	if second.Round != 1 || second.Status != store.DispatchReported || !strings.Contains(string(second.Result), "second") {
		t.Fatalf("rework dispatch = round %d %s %s", second.Round, second.Status, second.Result)
	}

	// The same Session took it: one launch, one Nudge, no second pane.
	if n := len(g.fake.Launches()); n != 1 {
		t.Fatalf("%d Sessions launched for a rework, want the same one", n)
	}
	nudges := g.fake.Nudges()
	if len(nudges) != 1 {
		t.Fatalf("%d Nudges, want 1", len(nudges))
	}
	// The Nudge carries the amended brief the Works recorded for the run, and the run the Session now reports through.
	if !strings.Contains(nudges[0], "Rework round 1") || !strings.Contains(nudges[0], "missed tests") || !strings.Contains(nudges[0], "Add login") {
		t.Errorf("the Nudge lacks the amended brief:\n%s", nudges[0])
	}
	for _, line := range []string{"ARCHWRIGHT_RUN=" + second.RunID, "ARCHWRIGHT_RUN_TOKEN="} {
		if !strings.Contains(nudges[0], line) {
			t.Errorf("the Nudge does not say %q:\n%s", line, nudges[0])
		}
	}
	if second.RunID == first.RunID {
		t.Errorf("the rework reused run %s", first.RunID)
	}
	// It works where it worked: the same worktree, on the same branch.
	if second.Branch == "" || second.Branch != first.Branch {
		t.Errorf("branches %q then %q", first.Branch, second.Branch)
	}
	if !strings.Contains(nudges[0], "same worktree") && !strings.Contains(nudges[0], first.Branch) {
		t.Errorf("the Nudge does not say where the work is:\n%s", nudges[0])
	}
	// The rework run's timeline and record follow the Nudged turn; the first run's stay as they were.
	rec := g.ended(second.RunID)
	kinds, summaries := g.timeline(second.RunID)
	var states []string
	for i, k := range kinds {
		if k == "session" {
			states = append(states, summaries[i])
		}
	}
	if !slices.Equal(states, []string{"working", "idle"}) || rec.Status != "done" {
		t.Errorf("rework run: status %s, timeline %v %v", rec.Status, kinds, summaries)
	}
	eventually(t, "the rework run to be recorded with the Session's pane, idle", func() bool {
		rec, _ = g.st.GetRunRecord(second.RunID)
		old, _ := g.st.GetRunRecord(first.RunID)
		return rec.SessionState == "idle" && rec.PaneID != "" && rec.PaneID == old.PaneID
	})
	if sess.State() != hosted.StateIdle {
		t.Errorf("the Session is %s after the rework", sess.State())
	}
}

func TestReworkRunsAFreshSessionWithTheAmendedBriefWhenTheSessionIsGone(t *testing.T) {
	g := newSessionRig(t, reworkScript())
	g.send("p")
	g.waitReported("p")
	old := g.fake.Sessions()[0]
	waitSessionIdle(t, old)
	old.Cancel() // the pane is gone: killed, or the Works restarted
	old.Result()
	g.fake.SetScript(hosted.Turn(callsTool("report_result", `{"status":"done","result":"fresh","evidence":["go test"]}`)))

	g.rework("p")
	d := g.waitReported("p")
	if d.Round != 1 || !strings.Contains(string(d.Result), "fresh") {
		t.Fatalf("rework dispatch = round %d %s", d.Round, d.Result)
	}
	launches := g.fake.Launches()
	if len(launches) != 2 || len(g.fake.Nudges()) != 0 {
		t.Fatalf("%d launches and %d Nudges, want a fresh Session and no Nudge", len(launches), len(g.fake.Nudges()))
	}
	if b := launches[1].Brief; !strings.Contains(b, "Rework round 1") || !strings.Contains(b, "missed tests") || !strings.Contains(b, "Add login") {
		t.Errorf("the fresh Session was not given the amended brief:\n%s", b)
	}
	if launches[1].Env[spool.EnvRun] != d.RunID {
		t.Errorf("the fresh Session reports through %q, not its run %s", launches[1].Env[spool.EnvRun], d.RunID)
	}
}

func TestReworkFallbackSessionGetsTheGuildSkills(t *testing.T) {
	g := newSessionRig(t, reworkScript())
	root := t.TempDir()
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g.r.UseDefinitions(reg)
	g.r.UseSkills(SkillsDelivery{
		Catalogue: skills.Vendored(),
		Runner:    "claude",
		RunDir:    func(id string) string { return filepath.Join(root, "runs", id) },
	})
	g.send("p")
	g.waitReported("p")
	old := g.fake.Sessions()[0]
	waitSessionIdle(t, old)
	old.Cancel()
	old.Result()
	g.fake.SetScript(hosted.Turn(callsTool("report_result", `{"status":"done","result":"fresh","evidence":["go test"]}`)))

	g.rework("p")
	g.waitReported("p")
	launches := g.fake.Launches()
	if len(launches) != 2 {
		t.Fatalf("%d launches, want a fresh Session", len(launches))
	}
	if launches[1].Env["GIT_CONFIG_GLOBAL"] == "" {
		t.Error("the fresh Session was not given the run's git configuration for the Guild's skills")
	}
	if _, err := os.Stat(filepath.Join(launches[1].Dir, ".claude/skills/tdd/SKILL.md")); err != nil {
		t.Errorf("the fresh Session's worktree has no skills: %v", err)
	}
}

func TestReworkNeverNudgesASessionThatIsNotIdle(t *testing.T) {
	s := hosted.Turn(callsTool("report_result", `{"status":"done","result":"first","evidence":["go test"]}`))
	// After reporting, a human types in the pane and the agent asks for a permission.
	s.Steps = append(s.Steps,
		hosted.Step{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}},
		hosted.Step{Hook: &hosted.Hook{Event: hosted.HookPermissionReq, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"rm -rf build"}`)}},
	)
	g := newSessionRig(t, s)
	g.r.runner.(*SessionRunner).NudgeWait = 300 * time.Millisecond
	g.send("p")
	g.waitReported("p")
	sess := g.fake.Sessions()[0]
	eventually(t, "the Session to wait on a permission", func() bool { return sess.State() == hosted.StateWaitingOnPermission })

	g.rework("p")
	d := g.waitReported("p")
	if !strings.Contains(string(d.Result), "not idle") || !strings.Contains(string(d.Result), "waiting-on-permission") {
		t.Fatalf("result = %s", d.Result)
	}
	// Nothing was typed at the prompt, and no second Session was started beside the live one.
	if n := len(g.fake.Nudges()); n != 0 {
		t.Fatalf("%d Nudges reached a Session at a prompt", n)
	}
	if n := len(g.fake.Launches()); n != 1 {
		t.Fatalf("%d Sessions launched", n)
	}
	if sess.State() != hosted.StateWaitingOnPermission {
		t.Errorf("the Session is %s: it was disturbed", sess.State())
	}
}

func waitSessionIdle(t *testing.T, s hosted.Session) {
	t.Helper()
	eventually(t, "the Session to be idle", func() bool { return s.State() == hosted.StateIdle && s.Info().Turns > 0 })
}

// askingScript is a Wright Session that raises a blocking Ask and ends its
// turn, and takes the answer's Nudge as a new turn that reports "second".
func askingScript() hosted.Script {
	s := hosted.Turn(callsTool("ask", blockingAsk))
	s.Nudged = reworkScript().Nudged
	return s
}

// askedPiece sends a piece whose Wright raises blockingAsk, and returns the
// ask once the Wright's Session is idle with it open.
func (g *sessionRig) askedPiece(piece string) (store.Ask, hosted.Session) {
	g.t.Helper()
	g.send(piece)
	var ask store.Ask
	eventually(g.t, "the Wright's ask", func() bool {
		asks, _ := g.st.Asks(store.AskFilter{Job: g.job.ID})
		if len(asks) == 1 {
			ask = asks[0]
		}
		return len(asks) == 1
	})
	sess := g.fake.Sessions()[0]
	waitSessionIdle(g.t, sess)
	return ask, sess
}

func TestAnAnswerIsNudgedIntoTheAskersOpenSession(t *testing.T) {
	g := newSessionRig(t, askingScript())
	ask, sess := g.askedPiece("p")
	first, _ := g.st.LatestDispatch(g.job.ID, "p")

	// Raising the Ask did not end the Session: it waits at its prompt, with the question recorded and routed as before.
	if first.Status != store.DispatchBlocked || sess.State() != hosted.StateIdle {
		t.Fatalf("dispatch %s, Session %s while the Ask is open", first.Status, sess.State())
	}
	if ask.Holder != store.HolderWarden || ask.Status != store.AskOpen || !ask.Blocking {
		t.Fatalf("ask %+v", ask)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(g.fake.Nudges()); n != 0 || sess.State() != hosted.StateIdle {
		t.Fatalf("%d Nudges and Session %s with the Ask unanswered: it waits, and is never poked", n, sess.State())
	}
	var esc AskReceipt
	g.ok(g.warden, "escalate_ask", `{"ask_id":"`+ask.ID+`"}`, &esc)
	if _, err := g.r.AnswerAsk(esc.AskID, RoleArchwright, AnsweredByDashboard, []store.Answer{{QuestionID: "scheme", Values: []string{"jwt"}}, {QuestionID: "note", Values: []string{"use RS256"}}}); err != nil {
		t.Fatal(err)
	}

	second := g.waitReported("p")
	if second.Round != 1 || second.Status != store.DispatchReported || !strings.Contains(string(second.Result), "second") {
		t.Fatalf("after the answer: round %d %s %s", second.Round, second.Status, second.Result)
	}
	// The same Session took the answer: one launch, one Nudge, one pane.
	if n := len(g.fake.Launches()); n != 1 {
		t.Fatalf("%d Sessions launched for an answer, want the same one", n)
	}
	nudges := g.fake.Nudges()
	if len(nudges) != 1 {
		t.Fatalf("%d Nudges, want 1", len(nudges))
	}
	for _, want := range []string{"answered", "## Questions and answers", "Which auth scheme?", "A: jwt", "A: use RS256", "ARCHWRIGHT_RUN=" + second.RunID, "ARCHWRIGHT_RUN_TOKEN="} {
		if !strings.Contains(nudges[0], want) {
			t.Errorf("the Nudge lacks %q:\n%s", want, nudges[0])
		}
	}
	// It is not the amended brief of a rework, and the Session already holds the original.
	if strings.Contains(nudges[0], "sent back for rework") || strings.Contains(nudges[0], "Add login") {
		t.Errorf("the Nudge repeats the brief or calls the answer a rework:\n%s", nudges[0])
	}
	// It carries on where it was, on the same branch, and the answer is not a rework.
	if second.Branch == "" || second.Branch != first.Branch {
		t.Errorf("branches %q then %q", first.Branch, second.Branch)
	}
	if p, _ := g.st.GetPiece(g.job.ID, "p"); p.Reworks != 0 {
		t.Errorf("an answer counted as a rework: %+v", p)
	}
	if old, _ := g.st.GetDispatch(first.ID); old.Status != store.DispatchAnswered {
		t.Errorf("the asking dispatch is %s", old.Status)
	}
	eventually(t, "the answered run to be recorded with the Session's pane", func() bool {
		rec, _ := g.st.GetRunRecord(second.RunID)
		old, _ := g.st.GetRunRecord(first.RunID)
		return rec.SessionState == "idle" && rec.PaneID != "" && rec.PaneID == old.PaneID
	})
}

func TestAnAnswerStartsAFreshRunWithTheBriefAndTheQAWhenTheSessionIsGone(t *testing.T) {
	g := newSessionRig(t, askingScript())
	ask, old := g.askedPiece("p")
	old.Cancel() // the pane is gone while the Ask waits: killed, or the Works restarted
	old.Result()
	g.fake.SetScript(hosted.Turn(callsTool("report_result", `{"status":"done","result":"fresh","evidence":["go test"]}`)))

	g.ok(g.warden, "answer_ask", `{"ask_id":"`+ask.ID+`",`+bothAnswers[1:], nil)
	d := g.waitReported("p")
	if d.Round != 1 || !strings.Contains(string(d.Result), "fresh") {
		t.Fatalf("after the answer: round %d %s", d.Round, d.Result)
	}
	launches := g.fake.Launches()
	if len(launches) != 2 || len(g.fake.Nudges()) != 0 {
		t.Fatalf("%d launches and %d Nudges, want a fresh Session and no Nudge", len(launches), len(g.fake.Nudges()))
	}
	for _, want := range []string{"# Brief", "Add login", "## Questions and answers", "Which auth scheme?", "A: jwt", "A: use RS256"} {
		if !strings.Contains(launches[1].Brief, want) {
			t.Errorf("the fresh Session's brief lacks %q:\n%s", want, launches[1].Brief)
		}
	}
	if launches[1].Env[spool.EnvRun] != d.RunID {
		t.Errorf("the fresh Session reports through %q, not its run %s", launches[1].Env[spool.EnvRun], d.RunID)
	}
}

func TestAnAnswerNeverReachesASessionThatIsNotIdle(t *testing.T) {
	s := askingScript()
	// After asking, a human types in the pane and the agent asks for a permission.
	s.Steps = append(s.Steps,
		hosted.Step{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}},
		hosted.Step{Hook: &hosted.Hook{Event: hosted.HookPermissionReq, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"rm -rf build"}`)}},
	)
	g := newSessionRig(t, s)
	g.r.runner.(*SessionRunner).NudgeWait = 300 * time.Millisecond
	g.send("p")
	eventually(t, "the Session to wait on a permission", func() bool {
		ss := g.fake.Sessions()
		return len(ss) == 1 && ss[0].State() == hosted.StateWaitingOnPermission
	})
	asks, _ := g.st.Asks(store.AskFilter{Job: g.job.ID})
	g.ok(g.warden, "answer_ask", `{"ask_id":"`+asks[0].ID+`",`+bothAnswers[1:], nil)
	d := g.waitReported("p")
	if !strings.Contains(string(d.Result), "not idle") {
		t.Fatalf("result = %s", d.Result)
	}
	if n, l := len(g.fake.Nudges()), len(g.fake.Launches()); n != 0 || l != 1 {
		t.Fatalf("%d Nudges and %d launches: a Session at a prompt was typed at, or a second one started beside it", n, l)
	}
}
