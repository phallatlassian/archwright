package works

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/runner"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// agentRig dispatches Wrights through the real AgentRunner, with the stub CLIs
// of the runner package standing in for claude and codex.
type agentRig struct {
	*gateRig
	dir    string // where the stubs leave what they saw
	codex  RunEnv // a Warden of the codex-bound Guild
	locked RunEnv // a Warden of a codex-bound Guild that denies tools
}

var agentGuilds = map[string]Guild{
	"masonry":   {Name: "masonry", Binding: defs.Binding{Guild: "masonry", Runner: "claude", Access: defs.ReadWrite}},
	"carvers":   {Name: "carvers", Binding: defs.Binding{Guild: "carvers", Runner: "codex", Access: defs.ReadWrite}},
	"lockedout": {Name: "lockedout", Binding: defs.Binding{Guild: "lockedout", Runner: "codex", Access: defs.ReadWrite, DeniedTools: []string{"Bash"}}},
}

func newAgentRig(t *testing.T, fixture string) *agentRig {
	t.Helper()
	g := &agentRig{gateRig: newGateRig(t, store.Gates{}), dir: t.TempDir()}
	td, err := filepath.Abs("../runner/testdata")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARCHWRIGHT_STUB_ARGS", filepath.Join(g.dir, "args"))
	t.Setenv("ARCHWRIGHT_STUB_PWD", filepath.Join(g.dir, "pwd"))
	t.Setenv("ARCHWRIGHT_STUB_ENV", filepath.Join(g.dir, "env"))
	t.Setenv("ARCHWRIGHT_STUB_FIXTURE", fixture)
	env := map[string]string{EnvClaudeBinary: filepath.Join(td, "stub-claude.sh"), EnvCodexBinary: filepath.Join(td, "stub-codex.sh")}
	guilds := func(name string) (Guild, error) {
		if gd, ok := agentGuilds[name]; ok {
			return gd, nil
		}
		return Guild{}, errInvalidInput
	}
	g.r.Configure(guilds, WrightRunnerFrom(func(k string) string { return env[k] }, guilds, g.st))
	for guild, dst := range map[string]*RunEnv{"carvers": &g.codex, "lockedout": &g.locked} {
		if *dst, err = g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: guild}); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func (g *agentRig) send(warden RunEnv, piece string) DispatchInfo {
	g.t.Helper()
	var d DispatchInfo
	g.ok(warden, "dispatch_wright", `{"piece":"`+piece+`","brief":`+goodBrief+`}`, &d)
	return d
}

func (g *agentRig) file(name string) string {
	b, _ := os.ReadFile(filepath.Join(g.dir, name))
	return string(b)
}

// agentCalls plays the agent's tool calls from outside the stub CLI: once the
// stub is up and has recorded its environment, it calls report_result through
// the spool with it, then lets the stub exit.
func (g *agentRig) agentReports(result string) {
	waitFor := filepath.Join(g.dir, "release")
	g.t.Setenv("ARCHWRIGHT_STUB_WAITFOR", waitFor)
	go func() {
		var env spool.Env
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			vars := map[string]string{}
			for _, l := range strings.Split(g.file("env"), "\n") {
				if k, v, ok := strings.Cut(l, "="); ok {
					vars[k] = v
				}
			}
			if e, err := spool.EnvFrom(func(k string) string { return vars[k] }); err == nil {
				env = e
				break
			}
		}
		defer os.WriteFile(waitFor, nil, 0o600)
		if env.Run == "" {
			return
		}
		spool.Call(context.Background(), env, "rep-1", "report_result", json.RawMessage(result), 5*time.Second)
	}()
}

func (g *agentRig) wait(piece string) store.Dispatch {
	g.t.Helper()
	return g.waitReported(piece)
}

func TestWrightRunsOnClaudeInItsOwnWorktreeAndReportsThroughTheSpool(t *testing.T) {
	g := newAgentRig(t, "success.jsonl")
	g.agentReports(`{"status":"done","result":"branch x","evidence":["go test"]}`)
	g.send(g.warden, "p")
	d := g.wait("p")
	if d.Status != store.DispatchReported || !strings.Contains(string(d.Result), `"done"`) {
		t.Fatalf("dispatch = %s %s", d.Status, d.Result)
	}
	// It ran in a worktree of its own, not the integration one.
	wts, _ := g.st.Worktrees(g.job.ID)
	pwd := strings.TrimSpace(g.file("pwd"))
	found := false
	for _, w := range wts {
		if w.Kind == store.KindWright && w.Path != "" {
			if real, _ := filepath.EvalSymlinks(w.Path); real == pwd || w.Path == pwd {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("ran in %q, want a wright worktree of %+v", pwd, wts)
	}
	args := g.file("args")
	if !strings.Contains(args, "stream-json") || !strings.Contains(args, "--permission-mode\ndefault") || strings.Contains(args, "bypass") {
		t.Errorf("args = %q", args)
	}
	if !strings.Contains(args, "Add login") { // the rendered brief is the prompt
		t.Errorf("brief missing from %q", args)
	}
	if env := g.file("env"); !strings.Contains(env, "ARCHWRIGHT_RUN=") || !strings.Contains(env, "ARCHWRIGHT_RUN_TOKEN=") || !strings.Contains(env, "ARCHWRIGHT_SPOOL=") {
		t.Errorf("env = %q", env)
	}
	// The timeline, raw vendor events and usage reach the dashboard.
	rec := g.ended(d.RunID)
	if rec.Runner != "claude" || rec.InputTokens != 120 || rec.OutputTokens != 34 || rec.CostUSD != 0.0123 || rec.Status != "done" {
		t.Errorf("run record = %+v", rec)
	}
	evs, _ := g.st.RunEvents(d.RunID)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
		if !e.HasRaw {
			t.Errorf("event %d (%s) lost its raw content", e.Seq, e.Kind)
		}
	}
	if len(kinds) < 5 || kinds[0] != "posture" || kinds[len(kinds)-1] != "result" {
		t.Errorf("timeline = %v", kinds)
	}
}

func TestGuildBoundToCodexRunsOnCodex(t *testing.T) {
	g := newAgentRig(t, "codex-success.jsonl")
	g.agentReports(`{"status":"done","result":"branch x","evidence":["go test"]}`)
	g.send(g.codex, "p")
	d := g.waitReportedFor("p")
	if d.Status != store.DispatchReported {
		t.Fatalf("dispatch = %s %s", d.Status, d.Result)
	}
	if args := g.file("args"); !strings.HasPrefix(args, "exec\n") {
		t.Errorf("not run on codex: %q", args)
	}
	if rec := g.ended(d.RunID); rec.Runner != "codex" {
		t.Errorf("run record = %+v", rec)
	}
}

// ended waits for the run to be recorded as over: the dispatch is reported
// as soon as the spool call lands, while the process is still exiting.
func (g *agentRig) ended(run string) (rec store.RunRecord) {
	g.t.Helper()
	eventually(g.t, "the run to end", func() bool { rec, _ = g.st.GetRunRecord(run); return rec.Status != "running" })
	return rec
}

func (g *agentRig) waitReportedFor(piece string) store.Dispatch { return g.wait(piece) }

func TestRunThatEndsWithoutReportingFailsTheDispatchVisibly(t *testing.T) {
	g := newAgentRig(t, "success.jsonl")
	g.send(g.warden, "p")
	d := g.wait("p")
	if !strings.Contains(string(d.Result), `"failed"`) || !strings.Contains(string(d.Result), "ended without reporting") {
		t.Fatalf("result = %s", d.Result)
	}
}

func TestFailedAgentRunFailsTheDispatchWithTheVendorReason(t *testing.T) {
	g := newAgentRig(t, "failed.jsonl")
	g.send(g.warden, "p")
	d := g.wait("p")
	if !strings.Contains(string(d.Result), "claude run failed: error_max_turns") {
		t.Fatalf("result = %s", d.Result)
	}
}

func TestDeniedActionsFileAPermissionWantedItem(t *testing.T) {
	g := newAgentRig(t, "denied.jsonl")
	g.send(g.warden, "p")
	g.wait("p")
	items, _ := g.st.NeedsYou()
	var wanted int
	for _, it := range items {
		if it.Kind == store.NeedsPermissionWanted {
			wanted++
		}
	}
	if wanted != 2 {
		t.Fatalf("permission-wanted items = %d in %+v", wanted, items)
	}
}

func TestRateLimitRefusalPausesTheRunnerAndRequeues(t *testing.T) {
	g := newAgentRig(t, "ratelimit.jsonl")
	g.r.sched.backoffBase, g.r.sched.backoffMax = time.Hour, time.Hour
	g.send(g.warden, "p")
	eventually(t, "the runner to be paused", func() bool { return g.r.Queue().Paused != nil })
	q := g.r.Queue()
	if len(q.Running) != 0 || len(q.Queued) != 1 {
		t.Fatalf("queue = %+v", q)
	}
	d, _ := g.st.LatestDispatch(g.job.ID, "p")
	if d.Status != store.DispatchQueued || d.Result != nil {
		t.Fatalf("a rate-limited dispatch is requeued, not failed: %s %s", d.Status, d.Result)
	}
}

func TestCancellingAWrightStopsItsProcess(t *testing.T) {
	g := newAgentRig(t, "hang.jsonl")
	t.Setenv("ARCHWRIGHT_STUB_HOLD", "1")
	info := g.send(g.warden, "p")
	eventually(t, "the stub to start", func() bool { return g.file("env") != "" })
	if _, err := g.r.CancelDispatch(info.DispatchID); err != nil {
		t.Fatal(err)
	}
	d := g.wait("p")
	if !strings.Contains(string(d.Result), cancelledByDeveloper) {
		t.Fatalf("result = %s", d.Result)
	}
	if rec := g.ended(d.RunID); rec.Status != "cancelled" {
		t.Errorf("run record = %+v", rec)
	}
}

func TestGuildThatDeniesToolsFailsClosedOnCodex(t *testing.T) {
	g := newAgentRig(t, "codex-success.jsonl")
	g.send(g.locked, "p")
	d := g.wait("p")
	if !strings.Contains(string(d.Result), "cannot enforce") {
		t.Fatalf("result = %s", d.Result)
	}
	if g.file("args") != "" {
		t.Error("the runner was spawned")
	}
}

func TestRequestThatWouldWidenPermissionsIsRefusedBeforeSpawn(t *testing.T) {
	g := newAgentRig(t, "success.jsonl")
	td, _ := filepath.Abs("../runner/testdata")
	ar := &AgentRunner{
		Runners: map[string]runner.Runner{"claude": &runner.Claude{Binary: filepath.Join(td, "stub-claude.sh")}},
		Guilds:  func(string) (Guild, error) { return agentGuilds["masonry"], nil },
	}
	err := ar.Run(context.Background(), RunRequest{Guild: "masonry", Worktree: t.TempDir(), Brief: "b", ExtraEnv: map[string]string{"CLAUDE_CONFIG_DIR": "/elsewhere"}})
	if !errors.Is(err, runner.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	if g.file("args") != "" {
		t.Error("the runner was spawned")
	}
}

func TestWrightWithoutAWorktreeIsNotRun(t *testing.T) {
	ar := &AgentRunner{Runners: map[string]runner.Runner{"claude": &runner.Claude{Binary: "/nonexistent"}}, Guilds: func(string) (Guild, error) { return agentGuilds["masonry"], nil }}
	if err := ar.Run(context.Background(), RunRequest{Guild: "masonry", Brief: "b"}); err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Fatalf("err = %v", err)
	}
}

func TestFakeRunnerStaysSelectable(t *testing.T) {
	get := func(k string) string {
		if k == EnvWrightRunner {
			return "fake"
		}
		return ""
	}
	if _, ok := WrightRunnerFrom(get, nil, nil).(*FakeRunner); !ok {
		t.Fatal("the fake runner was not selected")
	}
	if _, ok := WrightRunnerFrom(func(string) string { return "" }, nil, nil).(*AgentRunner); !ok {
		t.Fatal("real runners are the default")
	}
}
