package works

import (
	"github.com/phallatlassian/archwright/internal/launch"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

// The tracer end to end: a dispatched Wright launched by the real tmux adapter
// in a real pane, a scripted stand-in for claude playing its hooks through the
// real `archwright session-hook` and reporting through the real
// `archwright tool report_result`, and the Works following it. Only claude is
// not real.
func TestWrightRunsAsASessionInARealTmuxPaneAndReportsThroughTheSpool(t *testing.T) {
	runSessionE2E(t, "claude")
}

// The same for a Wright of a Guild bound to codex: its hooks come from the
// stable file in the Works' Codex home, and it is a Codex Session in its pane.
func TestCodexWrightRunsAsASessionInARealTmuxPaneAndReportsThroughTheSpool(t *testing.T) {
	runSessionE2E(t, "codex")
}

// e2eRig is the Works dispatching Wrights onto the real tmux adapter, with the
// scripted stand-in for the vendor's CLI.
type e2eRig struct {
	*gateRig
	tmux, sock, short string
	tm                *hosted.Tmux
	sr                *SessionRunner
	newTmux           func() *hosted.Tmux // another adapter over the same server and files: a Works that started again
	guilds            GuildSource
}

func newSessionE2E(t *testing.T, vendor, standinScript string) *e2eRig {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed: Wrights were NOT run in a real pane")
	}
	short, err := os.MkdirTemp("", "aw")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(short, "t.sock")
	if out, err := exec.Command(tmuxBin, "-S", sock, "-f", "/dev/null", "new-session", "-d", "-s", "probe").CombinedOutput(); err != nil {
		os.RemoveAll(short)
		t.Skipf("tmux cannot start a server here (%v: %s): Wrights were NOT run in a real pane", err, strings.TrimSpace(string(out)))
	}
	exec.Command(tmuxBin, "-S", sock, "kill-server").Run()

	bin := filepath.Join(short, "archwright")
	if out, err := exec.Command("go", "build", "-tags", "nodoctorgate", "-o", bin, "../../cmd/archwright").CombinedOutput(); err != nil {
		t.Fatalf("building archwright: %v\n%s", err, out)
	}
	standin, err := filepath.Abs("../session/testdata/stand-in-" + vendor + ".sh")
	if err != nil {
		t.Fatal(err)
	}
	// The tmux server inherits this environment, as the real one inherits the developer's.
	t.Setenv("ARCHWRIGHT_BIN", bin)
	t.Setenv("ARCHWRIGHT_STANDIN", standinScript)
	t.Setenv("ARCHWRIGHT_STANDIN_OUT", filepath.Join(short, "out"))

	newTmux := func() *hosted.Tmux {
		tm := &hosted.Tmux{
			Tmux: tmuxBin, HookCommand: []string{bin, "session-hook"}, Socket: sock,
			Dir: filepath.Join(short, "state"), SocketDir: filepath.Join(short, "h"), UserConf: "-", CancelGrace: time.Second,
			UserCodexHome: filepath.Join(short, "dev-codex"),
		}
		if vendor == "codex" {
			tm.CodexBin = standin
		} else {
			tm.Claude = standin
		}
		t.Cleanup(tm.Close)
		return tm
	}
	tm := newTmux()
	t.Cleanup(func() {
		exec.Command(tmuxBin, "-S", sock, "kill-server").Run()
		os.RemoveAll(short)
	})
	g := newGateRig(t, store.Gates{})
	guilds := func(name string) (Guild, error) {
		if gd, ok := agentGuilds[name]; ok {
			return gd, nil
		}
		return Guild{}, errInvalidInput
	}
	sr := &SessionRunner{
		Runners: map[string]hosted.Runner{"claude": tm, "codex": tm.Codex()},
		Guilds:  guilds,
		Record: func(run string, info hosted.Info, state hosted.State) {
			g.st.SetRunSession(run, sessionRecord(info, state))
		},
	}
	g.r.Configure(guilds, sr)
	return &e2eRig{gateRig: g, tmux: tmuxBin, sock: sock, short: short, tm: tm, sr: sr, newTmux: newTmux, guilds: guilds}
}

func runSessionE2E(t *testing.T, vendor string) {
	report := `{"status":"done","result":"branch-x","evidence":["go-test"]}`
	e := newSessionE2E(t, vendor, "hook SessionStart;hook UserPromptSubmit;tool report_result "+report+";hook Stop;hold")
	g, tmuxBin, sock, short := e.gateRig, e.tmux, e.sock, e.short
	warden := g.warden
	if vendor == "codex" {
		w, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: "carvers"}) // codex-bound
		if err != nil {
			t.Fatal(err)
		}
		warden = w
	}
	g.ok(warden, "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`, nil)
	d := g.waitReported("p")
	if d.Status != store.DispatchReported || !strings.Contains(string(d.Result), "branch-x") {
		out, _ := os.ReadFile(filepath.Join(short, "out", "tool.out"))
		t.Fatalf("dispatch = %s %s (report_result said %q)", d.Status, d.Result, out)
	}
	var rec store.RunRecord
	eventually(t, "the Session to be recorded idle", func() bool {
		rec, _ = g.st.GetRunRecord(d.RunID)
		return rec.SessionState == "idle"
	})
	// The pane id the Works stored is a live pane of the factory session, in the Job's window.
	panes, err := exec.Command(tmuxBin, "-S", sock, "list-panes", "-a", "-F", "#{pane_id} #{window_name} #{session_name}").Output()
	if err != nil || !strings.Contains(string(panes), rec.PaneID+" "+g.job.ID+" factory") {
		t.Fatalf("pane %q is not in %q (%v)", rec.PaneID, panes, err)
	}
	if vendor == "codex" {
		if _, err := os.Stat(filepath.Join(short, "state", "codex-home", "hooks.json")); err != nil {
			t.Errorf("the Codex Session had no stable hooks file: %v", err)
		}
		if !strings.HasPrefix(rec.SessionID, "codex-") {
			t.Errorf("session id %q is not the one Codex reported", rec.SessionID)
		}
	}
	if rec.SessionID == "" || !strings.HasPrefix(rec.TranscriptPath, "/tmp/stand-in-") {
		t.Errorf("run record = %+v", rec)
	}
	var states []string
	evs, _ := g.st.RunEvents(d.RunID)
	for _, e := range evs {
		if e.Kind == "session" {
			states = append(states, e.Summary)
		}
	}
	if strings.Join(states, ",") != "idle,working,idle" {
		t.Errorf("states on the timeline = %v", states)
	}
}

// A rework Nudges the Wright's real Session: the Works pastes a pointer to the
// amended brief into the pane and presses Enter once the Session is idle, the
// scripted agent takes it as a new turn, and its second report arrives through
// the new run, whose variables the Nudge named.
func TestReworkNudgesTheSameWrightSessionInARealTmuxPane(t *testing.T) {
	first := `{"status":"done","result":"first","evidence":["go-test"]}`
	second := `{"status":"done","result":"second","evidence":["go-test"]}`
	e := newSessionE2E(t, "claude", "hook SessionStart;hook UserPromptSubmit;tool report_result "+first+";hook Stop;readnudge;hook UserPromptSubmit;tool report_result "+second+";hook Stop;hold")
	g := e.gateRig
	g.ok(g.warden, "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`, nil)
	d0 := g.waitReported("p")
	var rec store.RunRecord
	eventually(t, "the Session to be recorded idle", func() bool {
		rec, _ = g.st.GetRunRecord(d0.RunID)
		return rec.SessionState == "idle"
	})

	g.rework("p")
	d1 := g.waitReported("p")
	if d1.Round != 1 || d1.Status != store.DispatchReported || !strings.Contains(string(d1.Result), "second") {
		out, _ := os.ReadFile(filepath.Join(e.short, "out", "tool.out"))
		t.Fatalf("rework dispatch = round %d %s %s (report_result said %q)", d1.Round, d1.Status, d1.Result, out)
	}
	// What was typed in the pane is a pointer to the file holding the amended brief and the new run's variables.
	typed, err := os.ReadFile(filepath.Join(e.short, "out", "prompt.in"))
	if err != nil || !strings.HasPrefix(string(typed), "Read the file ") {
		t.Fatalf("the pane was typed %q (%v)", typed, err)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(string(typed)), "Read the file "), " in full and carry out what it says.")
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "Rework round 1") || !strings.Contains(string(body), "export ARCHWRIGHT_RUN="+d1.RunID) {
		t.Fatalf("the Nudge file %s = %q (%v)", path, body, err)
	}
	// One pane served both rounds, and its record follows the new run.
	var rec1 store.RunRecord
	eventually(t, "the rework run to be recorded with the Session's pane", func() bool {
		rec1, _ = g.st.GetRunRecord(d1.RunID)
		return rec1.PaneID != "" && rec1.SessionState == "idle"
	})
	if rec1.PaneID != rec.PaneID || d1.Branch != d0.Branch {
		t.Errorf("panes %q then %q, branches %q then %q", rec.PaneID, rec1.PaneID, d0.Branch, d1.Branch)
	}
	panes, _ := exec.Command(e.tmux, "-S", e.sock, "list-panes", "-a", "-f", "#{==:#{@placeholder},}", "-F", "#{pane_id}").Output()
	if n := len(strings.Fields(string(panes))); n != 2 { // the factory's own window and the Wright's
		t.Errorf("%d panes: %q", n, panes)
	}
}

// panes lists the panes of the factory session as "window pane" lines.
func (e *e2eRig) panes(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command(e.tmux, "-S", e.sock, "list-panes", "-a", "-f", "#{==:#{@placeholder},}", "-F", "#{window_name} #{pane_id}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	return strings.Fields(string(out))
}

// The Works spawns the Archwright in a real pane of the `archwright` window,
// replacing the empty pane the factory session started with, and an inbox item
// reaches the idle Session as a Nudge pasted into that pane.
func TestWorksSpawnsTheArchwrightInTheArchwrightWindowAndNudgesItInARealTmuxPane(t *testing.T) {
	e := newSessionE2E(t, "claude", "hook SessionStart;hook UserPromptSubmit;hook Stop;readprompt;hook UserPromptSubmit;hook Stop;hold")
	g := e.gateRig
	g.r.UseArchwright(&ArchwrightHost{
		Runners:   map[string]hosted.Runner{"claude": e.tm},
		Choice:    func() (string, error) { return "claude", nil },
		Home:      filepath.Join(e.short, "home"),
		SpoolRoot: func(string) string { return filepath.Join(e.short, "spool") },
	})
	info, err := g.r.StartArchwright("")
	if err != nil || !info.Started {
		t.Fatalf("StartArchwright = %+v, %v", info, err)
	}
	if got := e.panes(t); len(got) != 2 || got[0] != "archwright" || got[1] != info.Pane {
		t.Fatalf("panes = %q, want the Archwright's alone in the archwright window", got)
	}
	eventually(t, "the Archwright to finish its first turn", func() bool {
		a := g.r.archwrightSession()
		return a != nil && a.s.Info().Turns == 1
	})
	if _, err := g.st.QueueCommand("instruct", "", "SECRET words", nil); err != nil {
		t.Fatal(err)
	}
	var typed []byte
	eventually(t, "the Nudge to reach the pane", func() bool {
		typed, _ = os.ReadFile(filepath.Join(e.short, "out", "prompt.in"))
		return len(typed) > 0
	})
	if strings.TrimSpace(string(typed)) != launch.Nudge {
		t.Errorf("the pane was typed %q, want %q", typed, launch.Nudge)
	}
	eventually(t, "the agent to take the Nudge as a prompt", func() bool { return g.r.archwrightSession().s.Info().Prompts == 2 })
}

// A Job's Warden is one pane for all its steps of a Guild: the second step is
// pasted into it, and the pane stays open until the Job leaves active work.
func TestStandingWardenIsOnePaneForTheStepsOfItsGuildInARealTmuxPane(t *testing.T) {
	handback := func(piece string) string {
		return "tool dispatch_wright {\"piece\":\"" + piece + "\",\"brief\":" + noSpaces(fakeWrightBrief) + "};sleep 1;tool accept_piece {\"piece\":\"" + piece + "\"};" +
			`tool handback {"status":"done","results":["r"],"checked":["c"],"rejected":[],"uncertainty":"none"}`
	}
	e := newSessionE2E(t, "claude", "hook SessionStart;hook UserPromptSubmit;"+handback("p1")+";hook Stop;readnudge;hook UserPromptSubmit;"+handback("p2")+";hook Stop;hold")
	g := e.gateRig
	g.r.Configure(func(string) (Guild, error) { return agentGuilds["masonry"], nil }, NewFakeRunner(nil))
	g.r.ConfigureWarden(e.sr)
	g.r.UseSessions(e.sr, time.Minute)
	arch := startRun(t, g.r, RoleArchwright)
	if _, err := g.st.SetPlan(g.job.ID, []store.Step{{ID: "one", Guild: "masonry", Title: "One"}, {ID: "two", Guild: "masonry", Title: "Two"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.DecidePlan(g.job.ID, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if resp := call(t, arch, "s-"+id, "start_step", `{"job":"`+g.job.ID+`","step":"`+id+`"}`); !resp.OK {
			t.Fatalf("start_step %s: %+v", id, resp.Error)
		}
		eventually(t, "step "+id+" to hand back", func() bool {
			sr, _ := g.st.GetStepRun(g.job.ID, id)
			if sr.Status == store.StepFailed {
				out, _ := os.ReadFile(filepath.Join(e.short, "out", "tool.out"))
				t.Fatalf("step %s failed: %s (last tool said %q)", id, sr.Reason, out)
			}
			return sr.Status == store.StepHandedBack
		})
	}
	var warden int
	for _, l := range e.panes(t) {
		if l == g.job.ID {
			warden++
		}
	}
	if warden != 1 {
		t.Fatalf("panes = %q: want one pane for the Job's Warden", e.panes(t))
	}
	// The Job ends: after the retention its Warden's pane is removed and its window with it.
	if err := g.st.CloseJob(g.job.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	left := map[string]time.Time{}
	eventually(t, "the Warden's Session to be idle", func() bool {
		return e.sr.CloseJob("none") == 0 && len(e.sr.OpenJobs()) == 1 && jobSessionsIdle(e.sr, g.job.ID)
	})
	g.r.reapSessions(left, time.Now())
	g.r.reapSessions(left, time.Now().Add(2*time.Minute))
	for _, l := range e.panes(t) {
		if l == g.job.ID {
			t.Fatalf("the Job's window is still there: %q", e.panes(t))
		}
	}
}

func jobSessionsIdle(sr *SessionRunner, job string) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	for _, ls := range sr.live {
		if ls.job == job && ls.s.State() != hosted.StateIdle {
			return false
		}
	}
	return true
}

// noSpaces is JSON with its inter-token spaces removed, which a stand-in's step list can carry.
func noSpaces(j string) string {
	var out strings.Builder
	in := false
	for _, r := range j {
		if r == '"' {
			in = !in
		}
		if r == ' ' && in {
			out.WriteString("_")
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// restartWorks is the Works stopping and starting again over the same database,
// the same tmux server and the same files, as ServeWith does it: its Sessions
// are left in their panes, and the new Works adopts what it finds.
func (e *e2eRig) restartWorks(t *testing.T) (*Runs, SessionRecovery) {
	t.Helper()
	g := e.gateRig
	g.r.UseSessions(e.sr, time.Hour)
	g.r.Close()
	e.tm.Close()
	if _, err := g.st.InterruptDispatches(); err != nil {
		t.Fatal(err)
	}
	tm := e.newTmux()
	sr := &SessionRunner{
		Runners: map[string]hosted.Runner{"claude": tm, "codex": tm.Codex()},
		Guilds:  e.guilds,
		Record: func(run string, info hosted.Info, state hosted.State) {
			g.st.SetRunSession(run, sessionRecord(info, state))
		},
	}
	r2 := NewRuns(g.st)
	r2.poll = 5 * time.Millisecond
	t.Cleanup(r2.Close)
	r2.UseSites(g.sm)
	r2.Configure(e.guilds, sr)
	r2.UseSessions(sr, time.Hour)
	rec, err := r2.RecoverSessions()
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Resume(); err != nil {
		t.Fatal(err)
	}
	g.r, e.tm, e.sr = r2, tm, sr
	return r2, rec
}

// A Works that stops leaves the Wright's real Session in its pane, and the next
// one adopts that pane by the id it recorded: the Wright finishes the turn it
// was in, and the Works then pastes into the same pane what it needs to carry on
// (the stored brief and the new run's variables), without a second Session.
func TestRestartedWorksAdoptsTheWrightsRealPaneAndNudgesItOn(t *testing.T) {
	second := `{"status":"done","result":"second","evidence":["go-test"]}`
	e := newSessionE2E(t, "claude", "hook SessionStart;hook UserPromptSubmit;sleep 2;hook Stop;readnudge;hook UserPromptSubmit;tool report_result "+second+";hook Stop;hold")
	g := e.gateRig
	g.ok(g.warden, "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`, nil)
	var d0 store.Dispatch
	var rec store.RunRecord
	eventually(t, "the Wright to be working in its pane", func() bool {
		d0, _ = g.st.LatestDispatch(g.job.ID, "p")
		rec, _ = g.st.GetRunRecord(d0.RunID)
		return rec.SessionState == "working" && rec.PaneID != ""
	})

	_, recovery := e.restartWorks(t)
	if recovery.Adopted != 1 || recovery.Lost != 0 || len(recovery.Orphans) != 0 {
		t.Fatalf("recovery = %+v", recovery)
	}
	if old, _ := g.st.GetDispatch(d0.ID); old.Status != store.DispatchInterrupted {
		t.Fatalf("the old dispatch is %s", old.Status)
	}

	var d1 store.Dispatch
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if d1, _ = g.st.LatestDispatch(g.job.ID, "p"); d1.Round == 1 && d1.Status != store.DispatchRunning && d1.Status != store.DispatchQueued {
			break
		}
	}
	if d1.Round != 1 || d1.Status != store.DispatchReported || !strings.Contains(string(d1.Result), "second") {
		out, _ := os.ReadFile(filepath.Join(e.short, "out", "tool.out"))
		t.Fatalf("dispatch = round %d %s %s (report_result said %q)", d1.Round, d1.Status, d1.Result, out)
	}
	typed, _ := os.ReadFile(filepath.Join(e.short, "out", "prompt.in"))
	path := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(string(typed)), "Read the file "), " in full and carry out what it says.")
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "The Works restarted") || !strings.Contains(string(body), "Add login") || !strings.Contains(string(body), "export ARCHWRIGHT_RUN="+d1.RunID) {
		t.Fatalf("the Nudge file %s = %q (%v)", path, body, err)
	}
	var rec1 store.RunRecord
	eventually(t, "the new run to be recorded on the adopted pane", func() bool {
		rec1, _ = g.st.GetRunRecord(d1.RunID)
		return rec1.PaneID == rec.PaneID && rec1.SessionState == "idle"
	})
	if n := len(e.panes(t)); n != 4 { // the factory's own window and the Wright's: "window pane" for each
		t.Errorf("panes = %q: one Wright pane served both runs", e.panes(t))
	}
}

// Cancelling a Wright interrupts its real Session, kills its pane after the
// grace, records the run cancelled and keeps the worktree.
func TestCancellingAWrightKillsItsRealPaneAndKeepsItsWorktree(t *testing.T) {
	e := newSessionE2E(t, "claude", "hook SessionStart;hook UserPromptSubmit;hold")
	g := e.gateRig
	g.ok(g.warden, "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`, nil)
	var d store.Dispatch
	var rec store.RunRecord
	eventually(t, "the Wright to be working in its pane", func() bool {
		d, _ = g.st.LatestDispatch(g.job.ID, "p")
		rec, _ = g.st.GetRunRecord(d.RunID)
		return rec.SessionState == "working" && rec.PaneID != ""
	})
	if _, err := g.r.CancelDispatch(d.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run to be recorded cancelled", func() bool { rec, _ = g.st.GetRunRecord(d.RunID); return rec.Status == "cancelled" })
	eventually(t, "the pane to be gone", func() bool {
		for _, l := range e.panes(t) {
			if l == rec.PaneID {
				return false
			}
		}
		return true
	})
	if _, err := os.Stat(rec.SessionDir); err != nil {
		t.Fatalf("the worktree was not kept: %v", err)
	}
}
