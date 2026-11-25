package works

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

// These tests go through the Works: dispatches, the Works' own stop and start
// and the dashboard's cancel, on the fake Session runner, whose Sessions stay
// in their "panes" across a Works that stops and another that starts over the
// same database. What a real tmux and a real vendor CLI do with these is the
// session package's tests' and the end-to-end tests' to show.

// held is a script step that keeps the agent working until release is closed
// (or the Session is cancelled).
func held(release <-chan struct{}) hosted.Step {
	return hosted.Step{Act: func(ctx context.Context, _ hosted.LaunchRequest) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}}
}

var (
	startHook  = hosted.Step{Hook: &hosted.Hook{Event: hosted.HookSessionStart, SessionID: "fake-session", TranscriptPath: "/fake/transcript.jsonl"}}
	promptHook = hosted.Step{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}}
)

func stopHook(msg string) hosted.Step {
	return hosted.Step{Hook: &hosted.Hook{Event: hosted.HookStop, LastAssistantMessage: msg}}
}

// reports is a script step: the agent reports its result through the spool.
var reports = hosted.Step{Act: callsTool("report_result", `{"status":"done","result":"branch x","evidence":["go test"]}`)}

// stop is the Works stopping, leaving its Sessions in their panes.
func (g *sessionRig) stop() {
	g.t.Helper()
	if sr, ok := g.r.runner.(*SessionRunner); ok && g.r.sessions == nil {
		g.r.UseSessions(sr, time.Hour)
	}
	g.r.Close()
}

// restart is the Works stopping and another starting over the same database,
// as ServeWith does it: the old one closes, leaving the Sessions in their
// panes; what it left running is interrupted; the new one takes the Sessions
// over, then resumes its queue.
func (g *sessionRig) restart() (*Runs, SessionRecovery) {
	g.t.Helper()
	g.stop()
	if _, err := g.st.InterruptDispatches(); err != nil {
		g.t.Fatal(err)
	}
	r2 := NewRuns(g.st)
	r2.poll = 5 * time.Millisecond
	g.t.Cleanup(r2.Close)
	r2.UseSites(g.sm)
	guilds := func(name string) (Guild, error) {
		if gd, ok := agentGuilds[name]; ok {
			return gd, nil
		}
		return Guild{}, errInvalidInput
	}
	sr := &SessionRunner{
		Runners: map[string]hosted.Runner{"claude": g.fake}, Guilds: guilds, Fallback: NewFakeRunner(nil),
		Record: func(run string, info hosted.Info, state hosted.State) {
			g.st.SetRunSession(run, sessionRecord(info, state))
		},
	}
	r2.Configure(guilds, sr)
	r2.UseSessions(sr, 0)
	rec, err := r2.RecoverSessions()
	if err != nil {
		g.t.Fatal(err)
	}
	if err := r2.Resume(); err != nil {
		g.t.Fatal(err)
	}
	g.r = r2
	return r2, rec
}

// workingWright dispatches p, and returns once its Session is working on it.
func (g *sessionRig) workingWright(piece string) store.Dispatch {
	g.t.Helper()
	g.send(piece)
	var d store.Dispatch
	eventually(g.t, "the Wright to be working", func() bool {
		d, _ = g.st.LatestDispatch(g.job.ID, piece)
		rec, err := g.st.GetRunRecord(d.RunID)
		return err == nil && rec.SessionState == string(hosted.StateWorking)
	})
	return d
}

func TestCancelInterruptsTheSessionKillsItRecordsTheRunCancelledAndKeepsTheWorktree(t *testing.T) {
	g := newSessionRig(t, hosted.Script{Steps: []hosted.Step{startHook, promptHook, held(nil)}})
	d := g.workingWright("p")
	wts, _ := g.st.Worktrees(g.job.ID)
	var wright store.Worktree
	for _, w := range wts {
		if w.Kind == store.KindWright {
			wright = w
		}
	}
	if wright.Path == "" {
		t.Fatalf("no Wright worktree: %+v", wts)
	}
	if err := os.WriteFile(wright.Path+"/unfinished.txt", []byte("half done"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := g.r.CancelDispatch(d.ID); err != nil {
		t.Fatal(err)
	}
	rec := g.ended(d.RunID)
	if rec.Status != "cancelled" || rec.SessionState != string(hosted.StateEnded) {
		t.Fatalf("run = %s, session %s; want cancelled and ended", rec.Status, rec.SessionState)
	}
	if s := g.fake.Sessions()[0]; s.Result().Status != hosted.StatusCancelled || s.State() != hosted.StateEnded {
		t.Fatalf("the Session was not cancelled: %+v", s.Result())
	}
	cur := g.waitReported("p")
	if !strings.Contains(string(cur.Result), `"failed"`) || !strings.Contains(string(cur.Result), cancelledByDeveloper) {
		t.Fatalf("dispatch = %s %s", cur.Status, cur.Result)
	}
	// The worktree is kept as the Wright left it, never removed, and the Works commits nothing in it.
	if b, err := os.ReadFile(wright.Path + "/unfinished.txt"); err != nil || string(b) != "half done" {
		t.Fatalf("the worktree lost the Wright's work: %q %v", b, err)
	}
	if got := git(t, wright.Path, "status", "--porcelain", "--", "unfinished.txt"); !strings.Contains(got, "unfinished.txt") {
		t.Errorf("the Works committed what the Wright left: status %q", got)
	}
}

func TestSessionThatEndsWithoutReportingFailsTheDispatchAndKeepsItsLastMessage(t *testing.T) {
	g := newSessionRig(t, hosted.Script{
		Steps: []hosted.Step{startHook, promptHook, stopHook("I could not get the migration to run: the table is locked.")},
		End:   true,
	})
	g.send("p")
	d := g.waitReported("p")
	if !strings.Contains(string(d.Result), `"failed"`) || !strings.Contains(string(d.Result), "the table is locked") {
		t.Fatalf("dispatch = %s %s", d.Status, d.Result)
	}
	rec := g.ended(d.RunID)
	if rec.Status != "failed" {
		t.Fatalf("run = %s", rec.Status)
	}
	kinds, summaries := g.timeline(d.RunID)
	if !slices.Contains(kinds, "message") || !slices.Contains(summaries, "I could not get the migration to run: the table is locked.") {
		t.Fatalf("timeline = %v %v", kinds, summaries)
	}
}

func TestPaneThatDiesWithoutAHandbackFailsTheDispatchAndKeepsItsLastMessage(t *testing.T) {
	g := newSessionRig(t, hosted.Script{
		Steps: []hosted.Step{startHook, promptHook, stopHook("halfway there"), {Act: func(context.Context, hosted.LaunchRequest) error {
			return context.DeadlineExceeded // the process exits with an error: a crash
		}}},
	})
	g.send("p")
	d := g.waitReported("p")
	if !strings.Contains(string(d.Result), `"failed"`) || !strings.Contains(string(d.Result), "halfway there") {
		t.Fatalf("result = %s", d.Result)
	}
	if rec := g.ended(d.RunID); rec.Status != "failed" {
		t.Fatalf("run = %s", rec.Status)
	}
}

func TestPromptAPersonTypesIsRecordedOnTheRunAndNeverBlocksIt(t *testing.T) {
	typed := hosted.Step{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}} // a prompt the Works never sent
	g := newSessionRig(t, hosted.Script{Steps: []hosted.Step{startHook, promptHook, typed, reports, stopHook("done")}})
	g.send("p")
	d := g.waitReported("p")
	if d.Status != store.DispatchReported {
		t.Fatalf("a person typing blocked the Wright: %s %s", d.Status, d.Result)
	}
	g.ended(d.RunID)
	kinds, summaries := g.timeline(d.RunID)
	n := 0
	for i, k := range kinds {
		if k == "human" {
			n++
			if !strings.Contains(summaries[i], "someone typed") {
				t.Errorf("human entry %q", summaries[i])
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d human entries in %v", n, kinds)
	}
}

// A Works that stops leaves the Session open; the next one adopts it by its
// recorded pane id, and the work carries on in the same Session: no second
// Session, the same worktree, the stored brief Nudged in once it is idle.
func TestRestartAdoptsTheLivePaneAndTheInterruptedWrightCarriesOnInIt(t *testing.T) {
	release := make(chan struct{})
	g := newSessionRig(t, hosted.Script{
		Steps:  []hosted.Step{startHook, promptHook, held(release), stopHook("paused for the restart")},
		Nudged: []hosted.Step{promptHook, reports, stopHook("done")},
	})
	d := g.workingWright("p")
	rec, _ := g.st.GetRunRecord(d.RunID)
	if rec.PaneID == "" || rec.SessionDir == "" || rec.Runner != "claude" {
		t.Fatalf("run record lacks what adopting needs: %+v", rec)
	}

	r2, recovery := g.restart()
	if recovery.Adopted != 1 || recovery.Lost != 0 || len(recovery.Orphans) != 0 {
		t.Fatalf("recovery = %+v", recovery)
	}
	if old, _ := g.st.GetDispatch(d.ID); old.Status != store.DispatchInterrupted {
		t.Fatalf("the interrupted dispatch is %s: stopping the Works must not fail what it left running", old.Status)
	}
	if rr, _ := g.st.GetRunRecord(d.RunID); rr.Status != "running" {
		t.Fatalf("the old run is %s", rr.Status)
	}
	if s := g.fake.Sessions()[0]; s.State() == hosted.StateEnded {
		t.Fatal("stopping the Works killed the Session")
	}

	close(release) // the agent finishes the turn it was in; the Works then tells it to carry on
	next := g.waitReportedRound("p", 1)
	if next.Status != store.DispatchReported {
		t.Fatalf("dispatch = %s %s", next.Status, next.Result)
	}
	if n := len(g.fake.Launches()); n != 1 {
		t.Fatalf("%d Sessions were launched: the adopted one was not used", n)
	}
	nudges := g.fake.Nudges()
	if len(nudges) != 1 || !strings.Contains(nudges[0], "The Works restarted") || !strings.Contains(nudges[0], "Add login") ||
		!strings.Contains(nudges[0], "export ARCHWRIGHT_RUN="+next.RunID) {
		t.Fatalf("nudges = %q", nudges)
	}
	// In the worktree and on the branch the interrupted round had.
	old, _ := g.st.GetDispatch(d.ID)
	if next.Branch != old.Branch || next.Branch == "" {
		t.Fatalf("branch %q, was %q", next.Branch, old.Branch)
	}
	if _, ok := r2.runner.(SessionReuser); !ok {
		t.Fatal("the new Works' runner cannot reuse Sessions")
	}
	g.ended(next.RunID)
	if kinds, _ := g.timeline(next.RunID); !slices.Contains(kinds, "session") {
		t.Fatalf("the adopted Session's states did not reach the new run's timeline: %v", kinds)
	}
}

// With its pane gone the Session is resumed by id, in the directory it worked
// in, and told what a live one would be.
func TestRestartResumesALostSessionByIdInItsWorktree(t *testing.T) {
	g := newSessionRig(t, hosted.Script{Steps: []hosted.Step{startHook, promptHook, held(nil)}})
	d := g.workingWright("p")
	old, _ := g.st.GetRunRecord(d.RunID)
	g.fake.SetScript(hosted.Turn(callsTool("report_result", `{"status":"done","result":"branch x","evidence":["go test"]}`)))

	g.stop()
	g.fake.Sessions()[0].Cancel() // the pane is lost: tmux killed, or the machine rebooted
	g.fake.Sessions()[0].Result()
	_, recovery := g.restart()
	if recovery.Resumable != 1 || recovery.Adopted != 0 {
		t.Fatalf("recovery = %+v", recovery)
	}
	next := g.waitReportedRound("p", 1)
	if next.Status != store.DispatchReported {
		t.Fatalf("dispatch = %s %s", next.Status, next.Result)
	}
	launches := g.fake.Launches()
	if len(launches) != 2 {
		t.Fatalf("%d launches", len(launches))
	}
	res := launches[1]
	if res.Resume != "fake-session" || res.Dir != old.SessionDir || !strings.Contains(res.Brief, "The Works restarted") || !strings.Contains(res.Brief, "Add login") {
		t.Fatalf("resume launch = %+v, was in %q", res, old.SessionDir)
	}
	g.ended(next.RunID)
	if _, summaries := g.timeline(next.RunID); !slices.ContainsFunc(summaries, func(s string) bool { return strings.Contains(s, "resumed the Session fake-session") }) {
		t.Fatalf("timeline = %v", summaries)
	}
}

// A Session the vendor cannot resume is run afresh from the stored brief.
func TestRestartRedispatchesFromTheStoredBriefWhenTheSessionCannotBeResumed(t *testing.T) {
	g := newSessionRig(t, hosted.Script{Steps: []hosted.Step{startHook, promptHook, held(nil)}})
	d := g.workingWright("p")
	before, _ := g.st.GetDispatch(d.ID)
	g.fake.SetScript(hosted.Script{NoResume: true, Steps: hosted.Turn(callsTool("report_result", `{"status":"done","result":"branch x","evidence":["go test"]}`)).Steps})

	g.stop()
	g.fake.Sessions()[0].Cancel()
	g.fake.Sessions()[0].Result()
	g.restart()
	next := g.waitReportedRound("p", 1)
	if next.Status != store.DispatchReported {
		t.Fatalf("dispatch = %s %s", next.Status, next.Result)
	}
	launches := g.fake.Launches()
	if len(launches) != 3 || launches[1].Resume == "" || launches[2].Resume != "" {
		t.Fatalf("launches = %+v", launches)
	}
	if !strings.HasPrefix(launches[2].Brief, before.Rendered) {
		t.Fatalf("the fresh Session was not given the stored brief:\n%s", launches[2].Brief)
	}
	g.ended(next.RunID)
	if _, summaries := g.timeline(next.RunID); !slices.ContainsFunc(summaries, func(s string) bool { return strings.Contains(s, "could not be resumed") }) {
		t.Fatalf("timeline = %v", summaries)
	}
}

// With no pane and nothing to resume by, only the stored brief is left.
func TestRestartRunsASessionThatNeverStartedAfreshFromTheStoredBrief(t *testing.T) {
	g := newSessionRig(t, hosted.Script{Steps: []hosted.Step{promptHook, held(nil)}}) // no SessionStart: no session id
	g.send("p")
	var d store.Dispatch
	eventually(t, "the Session to be launched", func() bool {
		d, _ = g.st.LatestDispatch(g.job.ID, "p")
		rec, err := g.st.GetRunRecord(d.RunID)
		return err == nil && rec.PaneID != ""
	})
	g.fake.SetScript(hosted.Turn(callsTool("report_result", `{"status":"done","result":"branch x","evidence":["go test"]}`)))
	g.stop()
	g.fake.Sessions()[0].Cancel()
	g.fake.Sessions()[0].Result()
	_, recovery := g.restart()
	if recovery.Lost != 1 {
		t.Fatalf("recovery = %+v", recovery)
	}
	if rec, _ := g.st.GetRunRecord(d.RunID); rec.SessionState != string(hosted.StateEnded) {
		t.Fatalf("a lost Session stayed on the books as %q", rec.SessionState)
	}
	next := g.waitReportedRound("p", 1)
	if next.Status != store.DispatchReported || len(g.fake.Launches()) != 2 || g.fake.Launches()[1].Resume != "" {
		t.Fatalf("dispatch = %s; launches %+v", next.Status, g.fake.Launches())
	}
}

func TestRestartFlagsPanesNoRunAccountsForAndNeverKillsThem(t *testing.T) {
	g := newSessionRig(t, hosted.Script{Steps: []hosted.Step{startHook, promptHook, held(nil)}})
	g.workingWright("p")
	g.fake.AddOrphan(hosted.Orphan{Pane: "%stray", Run: "run-gone", Role: "wright"})
	_, recovery := g.restart()
	if recovery.Adopted != 1 || len(recovery.Orphans) != 1 || recovery.Orphans[0].Pane != "%stray" {
		t.Fatalf("recovery = %+v", recovery)
	}
	if known := g.fake.Known(); !known[g.fake.Sessions()[0].Info().Pane] || known["%stray"] {
		t.Fatalf("the Works told the runner it knew %v", known)
	}
	evs, _ := g.st.EventsAfter(0, "", 1000)
	flagged := false
	for _, e := range evs {
		flagged = flagged || (e.Type == "session.orphan" && strings.Contains(string(e.Payload), "%stray"))
	}
	if !flagged {
		t.Fatal("the orphan was not recorded")
	}
	if g.fake.Sessions()[0].State() == hosted.StateEnded {
		t.Fatal("a Session was killed")
	}
}

// waitReportedRound is waitReported for the dispatch of a later round.
func (g *sessionRig) waitReportedRound(piece string, round int) store.Dispatch {
	g.t.Helper()
	var d store.Dispatch
	eventually(g.t, "round to report", func() bool {
		d, _ = g.st.LatestDispatch(g.job.ID, piece)
		return d.Round >= round && d.Status != store.DispatchRunning && d.Status != store.DispatchQueued
	})
	return d
}

// A cancel that arrives while the run waits for a busy Session to be idle still
// interrupts that Session and kills its pane.
func TestCancelWhileWaitingForABusySessionStillKillsIt(t *testing.T) {
	g := newSessionRig(t, hosted.Script{Steps: []hosted.Step{startHook, promptHook, held(nil)}})
	d := g.workingWright("p")
	g.restart() // the adopted Session is still working: the restarted dispatch waits for it
	var next store.Dispatch
	eventually(t, "the restarted dispatch to run", func() bool {
		next, _ = g.st.LatestDispatch(g.job.ID, "p")
		return next.Round == 1 && next.Status == store.DispatchRunning
	})
	if _, err := g.r.CancelDispatch(next.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Session to be cancelled", func() bool { return g.fake.Sessions()[0].State() == hosted.StateEnded })
	if res := g.fake.Sessions()[0].Result(); res.Status != hosted.StatusCancelled {
		t.Fatalf("result = %+v", res)
	}
	if rec := g.ended(next.RunID); rec.Status != "cancelled" {
		t.Fatalf("run = %s", rec.Status)
	}
	_ = d
}
