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

func runSessionE2E(t *testing.T, vendor string) {
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
	report := `{"status":"done","result":"branch-x","evidence":["go-test"]}`
	// The tmux server inherits this environment, as the real one inherits the developer's.
	t.Setenv("ARCHWRIGHT_BIN", bin)
	t.Setenv("ARCHWRIGHT_STANDIN", "hook SessionStart;hook UserPromptSubmit;tool report_result "+report+";hook Stop;hold")
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
