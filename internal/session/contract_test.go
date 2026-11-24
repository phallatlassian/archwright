package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/runner"
)

// scenario names what a Session does; each harness knows how to make its
// runner play it.
type scenario string

const (
	scenTurn        scenario = "turn"         // takes the brief, works, finishes its turn, stays open
	scenPermResolve scenario = "perm-resolve" // a prompt, a Stop while it is open, then the tool completes
	scenPermHold    scenario = "perm-hold"    // a prompt nobody answers
	scenEnds        scenario = "ends"         // finishes its turn and exits 0
	scenCrash       scenario = "crash"        // exits 3 after starting
	scenNoStart     scenario = "no-start"     // never sends a SessionStart
	scenDuplicates  scenario = "duplicates"   // every SessionStart and UserPromptSubmit is delivered twice, as Codex does
	scenQueue       scenario = "queue"        // busy until a message is queued, then Stops and takes it as a new turn
)

// harness runs the contract against one Runner.
type harness struct {
	name   string
	vendor string   // the vendor CLI the runner hosts: claude or codex
	modes  []string // the narrowing permission modes the vendor honours
	queue  bool     // the vendor can be messaged while busy
	runner Runner
	// request prepares a launch request for the scenario.
	request func(t *testing.T, s scenario) LaunchRequest
	// pane is true when the Session lives in a real pane the test can inspect.
	pane *paneInspector
	// queued checks what the vendor CLI was asked to deliver; nil when it cannot be.
	queued func(t *testing.T, s Session, text string)
}

var runSeq atomic.Int32

func runID() string { return fmt.Sprintf("run-%d", runSeq.Add(1)) }

func fakeHarness(vendor string) harness {
	f := NewFakeFor(vendor, Script{})
	return harness{name: "fake " + vendor, vendor: vendor, modes: vendorModes[vendor], queue: vendor == "codex", runner: f, request: func(t *testing.T, s scenario) LaunchRequest {
		start := Step{Hook: &Hook{Event: HookSessionStart, SessionID: "fake-session", TranscriptPath: "/fake/transcript.jsonl"}}
		prompt := Step{Hook: &Hook{Event: HookUserPromptSubmit, TurnID: "turn-1"}}
		stop := Step{Hook: &Hook{Event: HookStop, TurnID: "turn-1"}}
		bash := func(ev, cmd string) Step {
			return Step{Hook: &Hook{Event: ev, TurnID: "turn-1", ToolName: "Bash", ToolInput: []byte(`{"command":"` + cmd + `"}`), ToolUseID: "tu-Bash"}}
		}
		switch s {
		case scenTurn:
			f.SetScript(Script{Steps: []Step{start, prompt, bash(HookPreToolUse, "go test"), bash(HookPostToolUse, "go test"), stop}})
		case scenPermResolve:
			f.SetScript(Script{Steps: []Step{start, prompt, bash(HookPreToolUse, "rm -rf build"), bash(HookPermissionReq, "rm -rf build"), stop, bash(HookPostToolUse, "rm -rf build"), stop}})
		case scenPermHold:
			f.SetScript(Script{Steps: []Step{start, prompt, bash(HookPreToolUse, "rm -rf build"), bash(HookPermissionReq, "rm -rf build")}})
		case scenEnds:
			f.SetScript(Script{Steps: []Step{start, prompt, stop}, End: true})
		case scenCrash:
			f.SetScript(Script{Steps: []Step{start, {Act: func(context.Context, LaunchRequest) error { return errors.New("exited with status 3") }}}})
		case scenNoStart:
			f.SetScript(Script{NoStart: true, Steps: []Step{start}})
		case scenDuplicates:
			f.SetScript(Script{Steps: []Step{start, start, prompt, prompt, stop}})
		case scenQueue:
			second := Step{Hook: &Hook{Event: HookUserPromptSubmit, TurnID: "turn-2"}}
			stop2 := Step{Hook: &Hook{Event: HookStop, TurnID: "turn-2"}}
			f.SetScript(Script{Steps: []Step{start, prompt, {AwaitQueue: true}, stop, second, stop2}})
		}
		return LaunchRequest{Run: runID(), Job: "job-1", Role: "wright", Brief: "do it", Dir: t.TempDir()}
	}}
}

// TestContract runs the one contract suite against every Runner: the fake,
// and the tmux adapter on a real tmux with a scripted stand-in CLI.
func TestContract(t *testing.T) {
	for _, vendor := range []string{"claude", "codex"} {
		t.Run(vendor, func(t *testing.T) {
			t.Run("fake", func(t *testing.T) { runContract(t, fakeHarness(vendor)) })
			t.Run("tmux", func(t *testing.T) { runContract(t, newTmuxRig(t, vendor).harness()) })
		})
	}
}

// vendorModes are the permission modes that only narrow a vendor's default and
// that the vendor can enforce: Codex has no plan or dontAsk mode.
var vendorModes = map[string][]string{"claude": {"plan", "dontAsk"}, "codex": nil}

func collect(t *testing.T, s Session) []Event {
	t.Helper()
	var evs []Event
	timeout := time.After(30 * time.Second)
	ch := s.Events()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, e)
		case <-timeout:
			t.Fatal("event stream never closed")
		}
	}
}

// waitState waits until the Session is in want.
func waitState(t *testing.T, s Session, want State) {
	t.Helper()
	waitFor(t, "state "+string(want), func() bool { return s.State() == want })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// states are the states an event list moves through, in order.
func states(evs []Event) []State {
	var out []State
	for _, e := range evs {
		if e.Kind == KindState {
			out = append(out, e.State)
		}
	}
	return out
}

func sameStates(got []State, want ...State) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func runContract(t *testing.T, h harness) {
	launch := func(t *testing.T, s scenario) Session {
		t.Helper()
		sess, err := h.runner.Launch(context.Background(), h.request(t, s))
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		t.Cleanup(sess.Cancel)
		return sess
	}

	t.Run("posture is the first event, then the states a turn goes through", func(t *testing.T) {
		sess := launch(t, scenTurn)
		waitFor(t, "the turn to finish", func() bool { return sess.Info().Turns == 1 })
		sess.Cancel()
		evs := collect(t, sess)
		if len(evs) < 2 || evs[0].Kind != KindPosture || evs[0].Posture == nil {
			t.Fatalf("first event is not a posture: %+v", evs)
		}
		if p := evs[0].Posture; p.Runner != h.vendor || p.Mode != "default" || p.Sandbox != "inherited" || p.Flags == nil || p.Notices == nil {
			t.Fatalf("posture = %+v", p)
		}
		for i, e := range evs {
			if e.Seq != i+1 {
				t.Fatalf("event %d has seq %d", i, e.Seq)
			}
		}
		if got := states(evs); !sameStates(got, StateIdle, StateWorking, StateIdle, StateEnded) {
			t.Fatalf("states = %v", got)
		}
	})

	t.Run("a Session reports where it lives and what the vendor told it", func(t *testing.T) {
		sess := launch(t, scenTurn)
		waitFor(t, "the turn to finish", func() bool { return sess.Info().Turns == 1 })
		info := sess.Info()
		if info.Pane == "" || info.Window == "" || info.SessionID == "" || info.TranscriptPath == "" {
			t.Fatalf("info = %+v", info)
		}
	})

	t.Run("events replay for a late subscriber", func(t *testing.T) {
		sess := launch(t, scenEnds)
		first := collect(t, sess)
		second := collect(t, sess)
		if len(first) != len(second) {
			t.Fatalf("replay has %d events, first read %d", len(second), len(first))
		}
	})

	t.Run("a permission request is waiting until its tool completes, even through a Stop", func(t *testing.T) {
		sess := launch(t, scenPermResolve)
		waitFor(t, "the Session to finish its turn", func() bool { return sess.Info().Turns == 1 })
		sess.Cancel()
		evs := collect(t, sess)
		if got := states(evs); !sameStates(got, StateIdle, StateWorking, StateWaitingOnPermission, StateWorking, StateIdle, StateEnded) {
			t.Fatalf("states = %v", got)
		}
		for _, e := range evs {
			if e.State == StateWaitingOnPermission && (e.Tool != "Bash" || e.Command != "rm -rf build") {
				t.Errorf("the permission request names %q %q", e.Tool, e.Command)
			}
		}
	})

	t.Run("a Session at a prompt is waiting, and cancel types nothing at it", func(t *testing.T) {
		sess := launch(t, scenPermHold)
		waitState(t, sess, StateWaitingOnPermission)
		sess.Cancel()
		if res := sess.Result(); res.Status != runner.StatusCancelled {
			t.Fatalf("result = %+v", res)
		}
		if h.pane != nil && h.pane.answered() {
			t.Fatal("the prompt received input: it was approved for the agent")
		}
	})

	t.Run("a Session that exits ends with its exit status", func(t *testing.T) {
		sess := launch(t, scenEnds)
		collect(t, sess)
		if res := sess.Result(); res.Status != runner.StatusDone || res.ExitStatus != 0 || sess.State() != StateEnded {
			t.Fatalf("result = %+v, state %s", res, sess.State())
		}
		crash := launch(t, scenCrash)
		collect(t, crash)
		if res := crash.Result(); res.Status != runner.StatusFailed || res.Error == "" {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("cancel ends a held Session as cancelled", func(t *testing.T) {
		sess := launch(t, scenTurn)
		waitFor(t, "the turn to finish", func() bool { return sess.Info().Turns == 1 })
		sess.Cancel()
		collect(t, sess)
		if res := sess.Result(); res.Status != runner.StatusCancelled || sess.State() != StateEnded {
			t.Fatalf("result = %+v, state %s", res, sess.State())
		}
		if h.pane != nil && h.pane.exists(sess.Info().Pane) {
			t.Fatal("the pane survived a cancel")
		}
	})

	t.Run("a Session that never starts fails to start", func(t *testing.T) {
		sess := launch(t, scenNoStart)
		res := sess.Result()
		if res.Status != runner.StatusFailed || !strings.Contains(res.Error, "failed to start") {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("duplicate deliveries of the same hook change nothing", func(t *testing.T) {
		sess := launch(t, scenDuplicates)
		waitFor(t, "the turn to finish", func() bool { return sess.Info().Turns == 1 })
		sess.Cancel()
		evs := collect(t, sess)
		if got := states(evs); !sameStates(got, StateIdle, StateWorking, StateIdle, StateEnded) {
			t.Fatalf("states = %v", got)
		}
		if n := sess.Info().Turns; n != 1 {
			t.Fatalf("turns = %d", n)
		}
	})

	t.Run("a Session that cannot be queued says so", func(t *testing.T) {
		sess := launch(t, scenPermHold)
		waitState(t, sess, StateWaitingOnPermission)
		q, ok := sess.(Queuer)
		if h.queue != ok {
			t.Fatalf("queue capability = %v, Session implements Queuer = %v", h.queue, ok)
		}
		if h.runner.Capabilities().Queue != h.queue {
			t.Fatalf("capabilities = %+v", h.runner.Capabilities())
		}
		if ok {
			if err := q.Queue("hello"); !errors.Is(err, ErrNotQueueable) {
				t.Fatalf("queue at a prompt = %v", err)
			}
		}
	})

	if h.queue {
		t.Run("a busy Session is queued a message that arrives as a new turn after its Stop", func(t *testing.T) {
			sess := launch(t, scenQueue)
			waitState(t, sess, StateWorking)
			if err := sess.(Queuer).Queue("and also run the linter"); err != nil {
				t.Fatalf("queue: %v", err)
			}
			waitFor(t, "the queued turn to finish", func() bool { return sess.Info().Turns == 2 })
			sess.Cancel()
			if got := states(collect(t, sess)); !sameStates(got, StateIdle, StateWorking, StateIdle, StateWorking, StateIdle, StateEnded) {
				t.Fatalf("states = %v", got)
			}
			if h.queued != nil {
				h.queued(t, sess, "and also run the linter")
			}
			if err := sess.(Queuer).Queue("too late"); !errors.Is(err, ErrNotQueueable) {
				t.Fatalf("queue to an ended Session = %v", err)
			}
		})
	}

	t.Run("forbidden spawn requests are refused before anything runs", func(t *testing.T) {
		before := 0
		if h.pane != nil {
			before = h.pane.panes()
		}
		for name, sp := range forbiddenSpawns {
			t.Run(name, func(t *testing.T) {
				req := h.request(t, scenTurn)
				req.Spawn = sp
				sess, err := h.runner.Launch(context.Background(), req)
				if !errors.Is(err, runner.ErrForbidden) || sess != nil {
					t.Fatalf("session = %v, err = %v", sess, err)
				}
			})
		}
		t.Run("foreign environment", func(t *testing.T) {
			req := h.request(t, scenTurn)
			for _, k := range []string{"ANTHROPIC_API_KEY", "CODEX_HOME"} {
				req.Env = map[string]string{k: "x"}
				if sess, err := h.runner.Launch(context.Background(), req); !errors.Is(err, runner.ErrForbidden) || sess != nil {
					t.Fatalf("%s: session = %v, err = %v", k, sess, err)
				}
			}
		})
		if h.pane != nil && h.pane.panes() != before {
			t.Fatalf("a refused launch left %d panes, had %d", h.pane.panes(), before)
		}
	})

	t.Run("a Guild may only narrow the mode", func(t *testing.T) {
		for _, mode := range h.modes {
			req := h.request(t, scenTurn)
			req.Spawn = runner.Spawn{Mode: mode}
			sess, err := h.runner.Launch(context.Background(), req)
			if err != nil {
				t.Fatalf("mode %s: %v", mode, err)
			}
			waitFor(t, "the posture", func() bool { return sess.State() != StateStarting })
			sess.Cancel()
			if p := collect(t, sess)[0].Posture; p == nil || p.Mode != mode {
				t.Fatalf("mode %s: posture = %+v", mode, p)
			}
		}
	})
}

var forbiddenSpawns = map[string]runner.Spawn{
	"bypassPermissions":   {Mode: "bypassPermissions"},
	"acceptEdits":         {Mode: "acceptEdits"},
	"auto":                {Mode: "auto"},
	"unknown mode":        {Mode: "yolo-mode"},
	"skip permissions":    {ExtraArgs: []string{"--dangerously-skip-permissions"}},
	"yolo":                {ExtraArgs: []string{"--yolo"}},
	"approve-for-me":      {ExtraArgs: []string{"--approve-for-me"}},
	"full access":         {ExtraArgs: []string{"--sandbox", "danger-full-access"}},
	"mode via flag":       {ExtraArgs: []string{"--permission-mode", "bypassPermissions"}},
	"mode via flag=":      {ExtraArgs: []string{"--permission-mode=acceptEdits"}},
	"settings override":   {Settings: "/tmp/s.json"},
	"settings flag":       {ExtraArgs: []string{"--settings=/tmp/s.json"}},
	"mcp override":        {MCPConfig: "/tmp/m.json"},
	"allow rule":          {AllowRules: []string{"Bash(rm *)"}},
	"allowedTools flag":   {ExtraArgs: []string{"--allowedTools", "Bash"}},
	"settings write":      {SettingsWrites: []string{"/etc/claude-code/managed-settings.json"}},
	"sandbox off":         {DisableSandbox: true},
	"sandbox off (value)": {ExtraArgs: []string{"dangerouslyDisableSandbox"}},
	"bypass hook trust":   {ExtraArgs: []string{"--dangerously-bypass-hook-trust"}},
	"config override":     {ExtraArgs: []string{"-c", "hooks.state={}"}},
	"sandbox flag":        {ExtraArgs: []string{"-s", "workspace-write"}},
}

// paneInspector reads the real tmux server a harness runs against.
type paneInspector struct {
	tmux   func(args ...string) (string, error)
	outDir string // where the stand-in leaves what it saw
}

func (p *paneInspector) panes() int {
	out, err := p.tmux("list-panes", "-a", "-F", "#{pane_id}")
	if err != nil || out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

func (p *paneInspector) exists(pane string) bool {
	out, err := p.tmux("list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		return false
	}
	for _, l := range strings.Fields(out) {
		if l == pane {
			return true
		}
	}
	return false
}

func (p *paneInspector) answered() bool {
	_, err := os.Stat(filepath.Join(p.outDir, "answered"))
	return err == nil
}
