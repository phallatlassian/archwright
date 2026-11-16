package works

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/runner"
)

// AgentRunner is the Runner that runs a Wright, Warden or reviewer as a real
// agent process on the vendor CLI its Guild is bound to (ADR 0003): claude or
// codex, picked per run, in the worktree the run is given. The agent reports through the spool
// tools like any Wright; this only starts it, mirrors its events to the
// dashboard and turns how it ended into the error the dispatch needs.
type AgentRunner struct {
	Runners map[string]runner.Runner // by the runner name a Guild binding gives
	Guilds  GuildSource
	Limits  runner.Limits
	// Denied is told of the actions the runner denied because they would have
	// prompted (ADR 0011); nil ignores them.
	Denied func(req RunRequest, denials []runner.Denial)
}

// Run implements Runner. The run's final state never depends on the agent's
// prose: it is the process's own result, and whether it reported through the
// spool, which the caller checks.
func (a *AgentRunner) Run(ctx context.Context, req RunRequest) error {
	g, err := a.Guilds(req.Guild)
	if err != nil {
		return err
	}
	r, ok := a.Runners[g.Binding.Runner]
	if !ok {
		return fmt.Errorf("guild %q is bound to runner %q, which is not available", g.Name, g.Binding.Runner)
	}
	if req.Worktree == "" {
		return fmt.Errorf("the run has no worktree to work in")
	}
	spawn, err := spawnFor(g.Binding.Runner, g)
	if err != nil {
		return err
	}
	env := map[string]string{}
	for _, kv := range req.Env.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for k, v := range req.ExtraEnv {
		env[k] = v
	}
	run, err := r.Start(ctx, runner.StartRequest{Brief: req.Brief, Dir: req.Worktree, Limits: a.Limits, Env: env, Spawn: spawn})
	if err != nil {
		return fmt.Errorf("%s was not started: %w", r.Name(), err) // a never-list refusal fails the dispatch closed
	}
	for e := range run.Events() {
		req.Trace.Emit(string(e.Kind), summarize(e), rawOf(e))
	}
	res := run.Result()
	req.Trace.Consumed(r.Name(), res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CostUSD)
	if len(res.Denials) > 0 && a.Denied != nil {
		a.Denied(req, res.Denials)
	}
	switch res.Status {
	case runner.StatusCancelled:
		return context.Canceled
	case runner.StatusFailed:
		err := fmt.Errorf("%s run failed: %s", r.Name(), res.Error)
		if res.RateLimited {
			return &RateLimitError{RetryAfter: res.RetryAfter, Err: err}
		}
		return err
	}
	return nil
}

// spawnFor is the permission posture a Guild's binding asks for. It can only
// narrow what the runner would do; the adapter refuses anything the never-list
// forbids, and a restriction the runner cannot enforce fails the run.
func spawnFor(runnerName string, g Guild) (runner.Spawn, error) {
	var s runner.Spawn
	if g.Binding.Access == defs.ReadOnly {
		s.Mode = "plan"
	}
	if len(g.Binding.DeniedTools) > 0 {
		if runnerName != "claude" {
			return s, fmt.Errorf("guild %q denies tools, which %s cannot enforce", g.Name, runnerName)
		}
		s.ExtraArgs = append(s.ExtraArgs, "--disallowed-tools", strings.Join(g.Binding.DeniedTools, ","))
	}
	return s, nil
}

// rawOf is the vendor event behind a timeline entry; an event the adapter
// made itself, with no vendor event, is stored as the event.
func rawOf(e runner.Event) json.RawMessage {
	if len(e.Raw) > 0 {
		return e.Raw
	}
	b, _ := json.Marshal(e)
	return b
}

const summaryMax = 200

func summarize(e runner.Event) string {
	var s string
	switch e.Kind {
	case runner.KindPosture:
		if p := e.Posture; p != nil {
			s = fmt.Sprintf("%s %s: %s mode, %s sandbox", p.Runner, p.Version, p.Mode, p.Sandbox)
		}
	case runner.KindStarted:
		s = "session started"
	case runner.KindToolUse:
		s = strings.TrimSpace(e.Tool + ": " + e.Command)
	case runner.KindPermissionDenied:
		s = "denied " + strings.TrimSpace(e.Tool+": "+e.Command)
	case runner.KindToolResult:
		s = e.Text
		if e.IsError {
			s = "error: " + s
		}
	default:
		s = e.Text
	}
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > summaryMax {
		s = string(r[:summaryMax]) + "…"
	}
	return s
}
