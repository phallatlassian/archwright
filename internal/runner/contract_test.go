package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// scenario names what a contract run does; each harness knows how to make its
// runner play it.
type scenario string

const (
	scenPlain  scenario = "plain"  // answers and ends done
	scenDenied scenario = "denied" // would-prompt action denied, run still ends
	scenHang   scenario = "hang"   // runs until cancelled
)

type harness struct {
	name   string
	runner Runner
	// request prepares a start request for the scenario, or skips the test.
	request func(t *testing.T, s scenario) StartRequest
}

func fakeHarness() harness {
	f := NewFake(Script{})
	return harness{name: "fake", runner: f, request: func(t *testing.T, s scenario) StartRequest {
		switch s {
		case scenPlain:
			f.SetScript(Script{
				Events: []Event{{Kind: KindMessage, Text: "ok"}, {Kind: KindResult, Text: "ok"}},
				Result: Result{Final: "ok", Usage: Usage{InputTokens: 1, OutputTokens: 2}},
			})
		case scenDenied:
			d := Denial{Tool: "Bash", Command: "rm -rf build"}
			f.SetScript(Script{
				Events: []Event{{Kind: KindPermissionDenied, Tool: d.Tool, Command: d.Command}, {Kind: KindResult}},
				Result: Result{Final: "blocked", Denials: []Denial{d}},
			})
		case scenHang:
			f.SetScript(Script{Events: []Event{{Kind: KindMessage, Text: "thinking"}}, Hold: true})
		}
		return StartRequest{Brief: "do it", Dir: t.TempDir()}
	}}
}

var stubFixtures = map[scenario]string{scenPlain: "success.jsonl", scenDenied: "denied.jsonl", scenHang: "hang.jsonl"}

// stubHarness runs the real Claude adapter against a stub CLI that replays a
// recorded stream, so the adapter is exercised in every default test run.
func stubHarness(t *testing.T) harness {
	stub, err := filepath.Abs("testdata/stub-claude.sh")
	if err != nil {
		t.Fatal(err)
	}
	return harness{name: "claude-stub", runner: &Claude{Binary: stub, Grace: time.Second}, request: func(t *testing.T, s scenario) StartRequest {
		env := map[string]string{"ARCHWRIGHT_STUB_FIXTURE": stubFixtures[s]}
		if s == scenHang {
			env["ARCHWRIGHT_STUB_HOLD"] = "1"
		}
		return StartRequest{Brief: "do it", Dir: t.TempDir(), Env: env}
	}}
}

var codexStubFixtures = map[scenario]string{scenPlain: "codex-success.jsonl", scenDenied: "codex-denied.jsonl", scenHang: "codex-hang.jsonl"}

// codexStubHarness runs the real Codex adapter against a stub CLI that replays
// a recorded `codex exec --json` stream.
func codexStubHarness(t *testing.T) harness {
	stub, err := filepath.Abs("testdata/stub-codex.sh")
	if err != nil {
		t.Fatal(err)
	}
	return harness{name: "codex-stub", runner: &Codex{Binary: stub, Grace: time.Second}, request: func(t *testing.T, s scenario) StartRequest {
		env := map[string]string{"ARCHWRIGHT_STUB_FIXTURE": codexStubFixtures[s]}
		if s == scenHang {
			env["ARCHWRIGHT_STUB_HOLD"] = "1"
		}
		return StartRequest{Brief: "do it", Dir: t.TempDir(), Env: env}
	}}
}

// codexRealHarness runs the genuine codex CLI under the developer's own
// login, sandbox and managed policy. Opt-in like claude-real.
func codexRealHarness() harness {
	return harness{name: "codex-real", runner: &Codex{}, request: func(t *testing.T, s scenario) StartRequest {
		switch s {
		case scenPlain:
			return StartRequest{Brief: "Reply with exactly the word ok and nothing else. Use no tools.", Dir: t.TempDir()}
		case scenHang:
			return StartRequest{Brief: "Write a 5000 word essay on the history of tea, slowly.", Dir: t.TempDir()}
		}
		t.Skip("a real run cannot be made to hit a permission denial deterministically")
		return StartRequest{}
	}}
}

// realHarness runs the genuine claude CLI. It costs money and needs a login,
// so it runs only when ARCHWRIGHT_REAL_RUNNERS=1 and never in CI.
func realHarness() harness {
	return harness{name: "claude-real", runner: &Claude{}, request: func(t *testing.T, s scenario) StartRequest {
		switch s {
		case scenPlain:
			return StartRequest{Brief: "Reply with exactly the word ok and nothing else. Use no tools.", Dir: t.TempDir(), Limits: Limits{MaxTurns: 2, MaxBudgetUSD: 0.25}}
		case scenHang:
			return StartRequest{Brief: "Write a 5000 word essay on the history of tea, slowly.", Dir: t.TempDir(), Limits: Limits{MaxBudgetUSD: 0.25}}
		}
		t.Skip("a real run cannot be made to hit a permission denial deterministically")
		return StartRequest{}
	}}
}

func TestContract(t *testing.T) {
	harnesses := []harness{fakeHarness(), stubHarness(t), codexStubHarness(t)}
	if os.Getenv("ARCHWRIGHT_REAL_RUNNERS") == "1" {
		harnesses = append(harnesses, realHarness(), codexRealHarness())
	}
	for _, h := range harnesses {
		t.Run(h.name, func(t *testing.T) { runContract(t, h) })
	}
}

func collect(t *testing.T, r Run) []Event {
	t.Helper()
	var evs []Event
	timeout := time.After(60 * time.Second)
	ch := r.Events()
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

func runContract(t *testing.T, h harness) {
	start := func(t *testing.T, s scenario) Run {
		t.Helper()
		run, err := h.runner.Start(context.Background(), h.request(t, s))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		return run
	}

	t.Run("posture is the first event and every event is numbered", func(t *testing.T) {
		run := start(t, scenPlain)
		evs := collect(t, run)
		if len(evs) < 2 || evs[0].Kind != KindPosture || evs[0].Posture == nil {
			t.Fatalf("first event is not a posture: %+v", evs)
		}
		p := evs[0].Posture
		if p.Runner != h.runner.Name() || p.Mode != "default" || p.Sandbox != "inherited" || p.Flags == nil || p.Notices == nil {
			t.Fatalf("posture = %+v", p)
		}
		for i, e := range evs {
			if e.Seq != i+1 {
				t.Fatalf("event %d has seq %d", i, e.Seq)
			}
		}
		postures := 0
		for _, e := range evs {
			if e.Kind == KindPosture {
				postures++
			}
		}
		if postures != 1 {
			t.Fatalf("%d posture events", postures)
		}
	})

	t.Run("result follows the stream", func(t *testing.T) {
		run := start(t, scenPlain)
		collect(t, run)
		res := run.Result()
		if res.Status != StatusDone || res.Final == "" {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("events replay for a late subscriber", func(t *testing.T) {
		run := start(t, scenPlain)
		first := collect(t, run)
		second := collect(t, run)
		if len(first) != len(second) {
			t.Fatalf("replay has %d events, first read %d", len(second), len(first))
		}
	})

	t.Run("a denied action is a permission_denied event with the exact command", func(t *testing.T) {
		run := start(t, scenDenied)
		evs := collect(t, run)
		var got *Event
		for i := range evs {
			if evs[i].Kind == KindPermissionDenied {
				got = &evs[i]
				break
			}
		}
		if got == nil || got.Tool == "" || got.Command == "" {
			t.Fatalf("no usable permission_denied event: %+v", evs)
		}
		res := run.Result()
		if len(res.Denials) == 0 || res.Denials[0].Command != got.Command {
			t.Fatalf("result denials = %+v, event command %q", res.Denials, got.Command)
		}
	})

	t.Run("cancel ends the run as cancelled", func(t *testing.T) {
		run := start(t, scenHang)
		time.Sleep(300 * time.Millisecond)
		run.Cancel()
		collect(t, run)
		if res := run.Result(); res.Status != StatusCancelled {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("forbidden spawn requests are refused before anything runs", func(t *testing.T) {
		for name, sp := range forbiddenSpawns {
			t.Run(name, func(t *testing.T) {
				req := h.request(t, scenPlain)
				req.Spawn = sp
				run, err := h.runner.Start(context.Background(), req)
				if !errors.Is(err, ErrForbidden) || run != nil {
					t.Fatalf("run = %v, err = %v", run, err)
				}
			})
		}
	})
}

var forbiddenSpawns = map[string]Spawn{
	"bypassPermissions":   {Mode: "bypassPermissions"},
	"acceptEdits":         {Mode: "acceptEdits"},
	"auto":                {Mode: "auto"},
	"unknown mode":        {Mode: "yolo-mode"},
	"skip permissions":    {ExtraArgs: []string{"--dangerously-skip-permissions"}},
	"yolo":                {ExtraArgs: []string{"--yolo"}},
	"approve-for-me":      {ExtraArgs: []string{"--approve-for-me"}},
	"full access":         {ExtraArgs: []string{"--sandbox", "danger-full-access"}},
	"full access (value)": {ExtraArgs: []string{"danger-full-access"}},
	"mode via flag":       {ExtraArgs: []string{"--permission-mode", "bypassPermissions"}},
	"mode via flag=":      {ExtraArgs: []string{"--permission-mode=acceptEdits"}},
	"settings override":   {Settings: "/tmp/s.json"},
	"settings flag":       {ExtraArgs: []string{"--settings=/tmp/s.json"}},
	"mcp override":        {MCPConfig: "/tmp/m.json"},
	"mcp flag":            {ExtraArgs: []string{"--mcp-config", "/tmp/m.json"}},
	"allow rule":          {AllowRules: []string{"Bash(rm *)"}},
	"allowedTools flag":   {ExtraArgs: []string{"--allowedTools", "Bash"}},
	"settings write":      {SettingsWrites: []string{"/etc/claude-code/managed-settings.json"}},
	"sandbox off":         {DisableSandbox: true},
	"sandbox off (value)": {ExtraArgs: []string{"dangerouslyDisableSandbox"}},
}

func TestCheckAllowsRestrictingModes(t *testing.T) {
	for _, m := range []string{"", "default", "plan", "dontAsk"} {
		if err := CheckRequest(StartRequest{Spawn: Spawn{Mode: m}}); err != nil {
			t.Errorf("mode %q refused: %v", m, err)
		}
	}
}

func TestCheckRefusesForeignEnvironment(t *testing.T) {
	for _, k := range []string{"HOME", "CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "XDG_CONFIG_HOME"} {
		if err := CheckRequest(StartRequest{Env: map[string]string{k: "x"}}); !errors.Is(err, ErrForbidden) {
			t.Errorf("env %s: %v", k, err)
		}
	}
	if err := CheckRequest(StartRequest{Env: map[string]string{"ARCHWRIGHT_RUN": "r"}}); err != nil {
		t.Error(err)
	}
}
