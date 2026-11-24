package session

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tmuxRig is a real tmux server on a throwaway socket with the scripted
// stand-in CLI in place of claude. The hook command is this test binary
// running the real SendHook.
type tmuxRig struct {
	vendor string
	tm     *Tmux
	bin    string
	sock   string
	dir    string
	out    string // where the stand-in leaves what it saw
}

// newTmuxRig skips, saying so, when tmux is missing or cannot start a server
// here (a sandbox that forbids unix sockets): the adapter is then not exercised.
func newTmuxRig(t *testing.T, vendor string) *tmuxRig {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed: the tmux adapter was NOT exercised")
	}
	dir, err := os.MkdirTemp("", "aw") // short: the sockets under it are limited to 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "t.sock")
	if out, err := exec.Command(bin, "-S", sock, "-f", "/dev/null", "new-session", "-d", "-s", "probe").CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		t.Skipf("tmux cannot start a server here (%v: %s): the tmux adapter was NOT exercised", err, strings.TrimSpace(string(out)))
	}
	exec.Command(bin, "-S", sock, "kill-server").Run()
	standin, err := filepath.Abs("testdata/stand-in-" + vendor + ".sh")
	if err != nil {
		t.Fatal(err)
	}
	r := &tmuxRig{vendor: vendor, bin: bin, sock: sock, dir: dir, out: filepath.Join(dir, "out")}
	r.tm = &Tmux{
		Tmux: bin, Socket: sock, HookCommand: []string{os.Args[0]},
		Dir: filepath.Join(dir, "state"), SocketDir: filepath.Join(dir, "h"), UserConf: "-",
		StartTimeout: 3 * time.Second, CancelGrace: time.Second, Poll: 30 * time.Millisecond,
		NudgeTimeout: time.Second, NudgePasteDelay: 20 * time.Millisecond,
		UserCodexHome: filepath.Join(dir, "dev-codex"),
	}
	if vendor == "codex" {
		r.tm.CodexBin = standin
	} else {
		r.tm.Claude = standin
	}
	t.Cleanup(func() {
		r.tm.Close()
		exec.Command(bin, "-S", sock, "kill-server").Run()
		os.RemoveAll(dir)
	})
	return r
}

// what the stand-in plays for each scenario
var standinScripts = map[scenario]string{
	scenTurn:        "hook SessionStart;hook UserPromptSubmit;hook PreToolUse Bash go test;hook PostToolUse Bash go test;hook Stop;hold",
	scenPermResolve: "hook SessionStart;hook UserPromptSubmit;hook PreToolUse Bash rm -rf build;hook PermissionRequest Bash rm -rf build;hook Stop;sleep 0.3;hook PostToolUse Bash rm -rf build;hook Stop;hold",
	scenPermHold:    "hook SessionStart;hook UserPromptSubmit;hook PreToolUse Bash rm -rf build;hook PermissionRequest Bash rm -rf build;prompt;hold",
	scenEnds:        "hook SessionStart;hook UserPromptSubmit;hook Stop;hook SessionEnd;exit 0",
	scenCrash:       "hook SessionStart;exit 3",
	scenNoStart:     "hold",
	scenDuplicates:  "hook SessionStart;again;hook UserPromptSubmit;again;hook Stop;hold",
	scenQueue:       "hook SessionStart;hook UserPromptSubmit;waitqueue;hook Stop;hook UserPromptSubmit;hook Stop;hold",
	scenNudge:       "hook SessionStart;hook UserPromptSubmit;hook Stop;readprompt;hook UserPromptSubmit;hook Stop;hold",
	scenDeaf:        "hook SessionStart;hook UserPromptSubmit;hook Stop;readprompt;hold",
	scenBusy:        "hook SessionStart;hook UserPromptSubmit;hold",
}

// runner is the Runner under test: the Tmux adapter for Claude, its Codex view for Codex.
func (r *tmuxRig) runner() Runner {
	if r.vendor == "codex" {
		return r.tm.Codex()
	}
	return r.tm
}

func (r *tmuxRig) request(t *testing.T, s scenario) LaunchRequest {
	t.Helper()
	return LaunchRequest{
		Run: runID(), Job: "job-1", Role: "wright", Brief: "do it", Dir: t.TempDir(),
		Env: map[string]string{"ARCHWRIGHT_STANDIN": standinScripts[s], "ARCHWRIGHT_STANDIN_OUT": r.out, "ARCHWRIGHT_TEST_HOOK": "1"},
	}
}

func (r *tmuxRig) harness() harness {
	h := harness{name: "tmux " + r.vendor, vendor: r.vendor, modes: vendorModes[r.vendor], queue: r.vendor == "codex", runner: r.runner(), request: r.request, pane: &paneInspector{tmux: r.tm.tmux, outDir: r.out}}
	h.nudged = func(t *testing.T, _ Session, want string) {
		t.Helper()
		if got := h.pane.typed(); got != want {
			t.Fatalf("the agent read %q, want %q", got, want)
		}
	}
	if h.queue {
		h.queued = func(t *testing.T, s Session, text string) {
			t.Helper()
			args, err := os.ReadFile(filepath.Join(r.tm.codexHome(), "queue-args"))
			if err != nil {
				t.Fatalf("codex queue was never run: %v", err)
			}
			want := "queue\n--thread\n" + s.Info().SessionID + "\n--message\n" + text + "\n"
			if string(args) != want {
				t.Fatalf("codex queue was run as %q, want %q", args, want)
			}
		}
	}
	return h
}

func (r *tmuxRig) launch(t *testing.T, req LaunchRequest) Session {
	t.Helper()
	s, err := r.runner().Launch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Cancel)
	return s
}

func (r *tmuxRig) saw(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.out, name))
	if err != nil {
		t.Fatalf("the stand-in left no %s: %v", name, err)
	}
	return string(b)
}

func TestTmuxLaysOutOneFactorySessionWithAWindowPerJobAndAPanePerSession(t *testing.T) {
	r := newTmuxRig(t, "claude")
	var sessions []Session
	for _, job := range []string{"job-a", "job-a", "job-b"} {
		req := r.request(t, scenTurn)
		req.Job = job
		sessions = append(sessions, r.launch(t, req))
	}
	for _, s := range sessions {
		waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	}
	if out, _ := r.tm.tmux("list-sessions", "-F", "#{session_name}"); out != "factory" {
		t.Fatalf("sessions = %q, want one factory session", out)
	}
	out, _ := r.tm.tmux("list-windows", "-t", "factory:", "-F", "#{window_name} #{window_panes}")
	got := strings.Fields(strings.ReplaceAll(out, "\n", " "))
	want := []string{"archwright", "1", "job-a", "2", "job-b", "1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("windows = %v, want %v", got, want)
	}
	for _, s := range sessions {
		info := s.Info()
		panes, _ := r.tm.tmux("list-panes", "-a", "-F", "#{pane_id} #{window_id} #{@run} #{@role}")
		if !strings.Contains(panes, info.Pane+" "+info.Window+" run-") || !strings.Contains(panes, " wright") {
			t.Errorf("pane %s of window %s is not in\n%s", info.Pane, info.Window, panes)
		}
	}
	if out, _ := r.tm.tmux("show-options", "-gv", "remain-on-exit"); out != "on" {
		t.Errorf("remain-on-exit = %q", out)
	}
}

func TestTmuxKeepsADeadPaneReadable(t *testing.T) {
	r := newTmuxRig(t, "claude")
	s := r.launch(t, r.request(t, scenEnds))
	if res := s.Result(); res.Status != "done" {
		t.Fatalf("result = %+v", res)
	}
	if out, err := r.tm.tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_dead}"); err != nil || !strings.Contains(out, s.Info().Pane+" 1") {
		t.Fatalf("the dead pane was not kept: %q %v", out, err)
	}
}

func TestTmuxCancelOfAnEndedSessionRemovesItsKeptPane(t *testing.T) {
	r := newTmuxRig(t, "claude")
	s := r.launch(t, r.request(t, scenEnds))
	if res := s.Result(); res.Status != "done" {
		t.Fatalf("result = %+v", res)
	}
	s.Cancel()
	if out, _ := r.tm.tmux("list-panes", "-a", "-F", "#{pane_id}"); strings.Contains(out, s.Info().Pane) {
		t.Fatalf("the ended Session's pane is still there: %q", out)
	}
}

func TestTmuxPlacesTheArchwrightInTheArchwrightWindowInPlaceOfTheEmptyPane(t *testing.T) {
	r := newTmuxRig(t, "claude")
	req := r.request(t, scenTurn)
	req.Role, req.Job = RoleArchwright, ""
	s := r.launch(t, req)
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	out, err := r.tm.tmux("list-panes", "-a", "-F", "#{window_name} #{pane_id}")
	if err != nil || out != "archwright "+s.Info().Pane {
		t.Fatalf("panes = %q (%v), want the Archwright's alone in the archwright window", out, err)
	}
	// Once it ends, its dead pane makes way for the next one.
	s.Cancel()
	s.Result()
	req2 := r.request(t, scenTurn)
	req2.Role, req2.Job = RoleArchwright, ""
	s2 := r.launch(t, req2)
	out, err = r.tm.tmux("list-panes", "-a", "-F", "#{window_name} #{pane_id}")
	if err != nil || out != "archwright "+s2.Info().Pane {
		t.Fatalf("panes = %q (%v)", out, err)
	}
}

func TestTmuxLaunchesClaudeInDefaultModeWithTheBriefAsThePositionalArgument(t *testing.T) {
	r := newTmuxRig(t, "claude")
	brief := "Fix it; don't `rm -rf $HOME`\nsecond \"line\" with 'quotes' and --flags"
	req := r.request(t, scenTurn)
	req.Brief, req.Limits = brief, Limits{Model: "sonnet"}
	s := r.launch(t, req)
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })

	args := r.saw(t, "args")
	for _, want := range []string{"--permission-mode\ndefault\n", "--settings\n", "--session-id\n", "--model\nsonnet\n", "\n--\n" + brief + "\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	for _, bad := range []string{"bypass", "dangerously", "yolo", "--bare", "approve-for-me"} {
		if strings.Contains(args, bad) {
			t.Errorf("args contain %q:\n%s", bad, args)
		}
	}
	if pwd, want := strings.TrimSpace(r.saw(t, "pwd")), mustReal(t, req.Dir); pwd != want {
		t.Errorf("ran in %q, want the worktree %q", pwd, want)
	}
	env := r.saw(t, "env")
	for _, want := range []string{"ARCHWRIGHT_RUN=" + req.Run, EnvHookSocket + "=", EnvHookFile + "=", EnvHookToken + "="} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
	// The id claude was told is the one its hooks report.
	if id := s.Info().SessionID; id == "" || !strings.Contains(args, "--session-id\n"+id+"\n") {
		t.Errorf("session id %q not in args:\n%s", id, args)
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestTmuxHooksSettingsOnlyObserve(t *testing.T) {
	r := newTmuxRig(t, "claude")
	req := r.request(t, scenTurn)
	s := r.launch(t, req)
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	b, err := os.ReadFile(filepath.Join(r.tm.Dir, "runs", req.Run, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]map[string][]struct {
		Hooks []struct{ Type, Command string }
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	var keys []string
	var raw map[string]json.RawMessage
	json.Unmarshal(b, &raw)
	for k := range raw {
		keys = append(keys, k)
	}
	if len(keys) != 1 || keys[0] != "hooks" {
		t.Fatalf("settings keys = %v: only hooks may be set, never permissions, allow rules or sandbox", keys)
	}
	for _, e := range hookEvents {
		groups := settings["hooks"][e]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 || groups[0].Hooks[0].Type != "command" || !strings.Contains(groups[0].Hooks[0].Command, os.Args[0]) {
			t.Errorf("hook %s = %+v", e, groups)
		}
	}
}

func TestTmuxPassesALongBriefAsAFile(t *testing.T) {
	r := newTmuxRig(t, "claude")
	req := r.request(t, scenTurn)
	req.Brief = strings.Repeat("a long brief line\n", 2000)
	s := r.launch(t, req)
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	path := filepath.Join(r.tm.Dir, "runs", req.Run, "brief.md")
	if got, err := os.ReadFile(path); err != nil || string(got) != req.Brief {
		t.Fatalf("brief file: %v, %d bytes", err, len(got))
	}
	if args := r.saw(t, "args"); !strings.Contains(args, "\n--\nYour brief is in the file "+path+".") || strings.Contains(args, "a long brief line") {
		t.Fatalf("args = %q", args)
	}
}

func TestTmuxHookFallsBackToTheSpoolWhenTheReceiverIsUnreachable(t *testing.T) {
	r := newTmuxRig(t, "claude")
	req := r.request(t, scenTurn)
	req.Env["ARCHWRIGHT_TEST_NO_SOCKET"] = "1"
	s := r.launch(t, req)
	waitFor(t, "a turn read from the spool", func() bool { return s.Info().Turns == 1 })
	if s.Info().SessionID == "" {
		t.Fatal("no session id from the spool")
	}
}

func TestTmuxCancelInterruptsThenKillsThePane(t *testing.T) {
	r := newTmuxRig(t, "claude")
	s := r.launch(t, r.request(t, scenTurn))
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	start := time.Now()
	s.Cancel()
	if res := s.Result(); res.Status != "cancelled" {
		t.Fatalf("result = %+v", res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancel took %s", d)
	}
}

func TestTmuxFailedToStartKeepsTheDeadPane(t *testing.T) {
	r := newTmuxRig(t, "claude")
	r.tm.StartTimeout = 500 * time.Millisecond
	s := r.launch(t, r.request(t, scenNoStart))
	res := s.Result()
	if res.Status != "failed" || !strings.Contains(res.Error, "failed to start") {
		t.Fatalf("result = %+v", res)
	}
	waitFor(t, "the process to stop and the pane to stay", func() bool {
		out, _ := r.tm.tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_dead}")
		return strings.Contains(out, s.Info().Pane+" 1")
	})
}

func TestTmuxRefusesAContextThatIsAlreadyCancelled(t *testing.T) {
	r := newTmuxRig(t, "claude")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := r.tm.Launch(ctx, r.request(t, scenTurn)); err == nil || s != nil {
		t.Fatalf("launch = %v, %v", s, err)
	}
}

func TestTmuxDefaultsToItsOwnSocketName(t *testing.T) {
	tm := &Tmux{}
	if got := strings.Join(tm.socketArgs(), " "); got != "-L archwright" {
		t.Fatalf("socket args = %q", got)
	}
	if tm.session() != "factory" {
		t.Fatalf("session = %q", tm.session())
	}
}
