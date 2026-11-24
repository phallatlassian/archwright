package works

import (
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
	t.Cleanup(func() {
		tm.Close()
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
	g.r.Configure(guilds, &SessionRunner{
		Runners: map[string]hosted.Runner{"claude": tm, "codex": tm.Codex()},
		Guilds:  guilds,
		Record: func(run string, info hosted.Info, state hosted.State) {
			g.st.SetRunSession(run, info.Pane, info.SessionID, info.TranscriptPath, string(state))
		},
	})
	return &e2eRig{gateRig: g, tmux: tmuxBin, sock: sock, short: short}
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
	panes, _ := exec.Command(e.tmux, "-S", e.sock, "list-panes", "-a", "-F", "#{pane_id}").Output()
	if n := len(strings.Fields(string(panes))); n != 2 { // the factory's own window and the Wright's
		t.Errorf("%d panes: %q", n, panes)
	}
}
