package works

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

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
