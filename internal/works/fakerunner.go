package works

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/phallatlassian/archwright/internal/spool"
)

// FakeRunner is a Runner that plays a script instead of running an agent. Each
// run of a piece consumes the piece's next scripted result, reported through
// the spool like a real Wright would; a piece with no script left reports done.
type FakeRunner struct {
	mu     sync.Mutex
	script map[string][]WrightResult
	asks   map[string][]string // per piece, the ask input a run raises instead of reporting
	briefs []RunRequest
	delay  time.Duration
	act    func(ctx context.Context, req RunRequest) error
}

// WithAct makes each run call fn after it starts and before it reports, so a
// test can play the part of a Wright using its worktree and tool calls.
func (f *FakeRunner) WithAct(fn func(ctx context.Context, req RunRequest) error) *FakeRunner {
	f.act = fn
	return f
}

// WithDelay makes each run wait before it reports, so a Wright can be in
// flight long enough to be interrupted.
func (f *FakeRunner) WithDelay(d time.Duration) *FakeRunner {
	f.delay = d
	return f
}

// ScriptAsk makes the piece's next run raise an ask with this JSON input and
// then end without reporting, as a Wright does when it blocks (ADR 0015).
func (f *FakeRunner) ScriptAsk(piece, input string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.asks == nil {
		f.asks = map[string][]string{}
	}
	f.asks[piece] = append(f.asks[piece], input)
}

// NewFakeRunner scripts results per piece, one per round.
func NewFakeRunner(script map[string][]WrightResult) *FakeRunner {
	return &FakeRunner{script: script}
}

// Requests returns every run started so far, with the brief each was given.
func (f *FakeRunner) Requests() []RunRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RunRequest(nil), f.briefs...)
}

// Run implements Runner.
func (f *FakeRunner) Run(ctx context.Context, req RunRequest) error {
	f.mu.Lock()
	f.briefs = append(f.briefs, req)
	var ask string
	if q := f.asks[req.Piece]; len(q) > 0 {
		ask, f.asks[req.Piece] = q[0], q[1:]
	}
	res := WrightResult{Status: ResultDone, Result: "fake://" + req.Piece, Evidence: []string{"fake runner"}}
	if q := f.script[req.Piece]; len(q) > 0 {
		res, f.script[req.Piece] = q[0], q[1:]
	}
	f.mu.Unlock()
	req.Trace.Emit("posture", "fake runner: default mode, inherited sandbox", json.RawMessage(`{"runner":"fake","mode":"default","sandbox":"inherited"}`))
	req.Trace.Emit("message", "Reading the brief for "+req.Piece, json.RawMessage(`{"type":"assistant","text":"Reading the brief for `+req.Piece+`"}`))
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.act != nil {
		if err := f.act(ctx, req); err != nil {
			return err
		}
	}
	if ask != "" {
		resp, err := spool.Call(ctx, spool.Env{Run: req.Env.Run, Dir: req.Env.Spool, Token: req.Env.Token}, "ask-"+req.DispatchID, "ask", json.RawMessage(ask), spool.DefaultWait)
		if err != nil {
			return err
		}
		if !resp.OK {
			return fmt.Errorf("ask: %s", resp.Error)
		}
		return nil
	}
	if isMergeBasePiece(req.Piece) && req.Worktree != "" {
		if err := fakeMerge(req); err != nil {
			return err
		}
	}
	req.Trace.Emit("tool_use", "report_result: "+string(res.Status), json.RawMessage(`{"type":"tool_use","name":"archwright tool report_result"}`))
	req.Trace.Consumed("fake", 1200, 300, 0.0123)
	in, err := json.Marshal(res)
	if err != nil {
		return err
	}
	resp, err := spool.Call(ctx, spool.Env{Run: req.Env.Run, Dir: req.Env.Spool, Token: req.Env.Token}, "report-"+req.DispatchID, "report_result", in, spool.DefaultWait)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("report_result: %s", resp.Error)
	}
	return nil
}

// fakeMerge plays a merge Wright: it merges the base ref named in the brief
// into the worktree, taking the integration side of every conflict, and
// commits. It only merges: no rebase, reset, amend or push.
func fakeMerge(req RunRequest) error {
	_, ref, ok := strings.Cut(req.Brief, "- base ref: ")
	ref, _, _ = strings.Cut(ref, "\n")
	if !ok || ref == "" {
		return fmt.Errorf("fake merge Wright: the brief names no base ref")
	}
	git := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-C", req.Worktree, "-c", "user.name=fake merge Wright", "-c", "user.email=fake@archwright.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("fake merge Wright: git %s: %v: %s", args[0], err, out)
		}
		return nil
	}
	if git("merge", "--no-edit", "-X", "ours", ref) == nil {
		return nil
	}
	if err := git("checkout", "--ours", "--", "."); err != nil { // what -X ours cannot settle: take our side whole
		return err
	}
	if err := git("add", "-A"); err != nil {
		return err
	}
	return git("commit", "--no-edit")
}
