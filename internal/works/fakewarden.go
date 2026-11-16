package works

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// fakeWrightBrief is a complete, valid brief for the one piece the fake Warden dispatches.
const fakeWrightBrief = `{"objective":"carry out the step","scope":{"in":["."],"out":[]},"constraints":[],` +
	`"inputs":["the step brief"],"definition_of_done":["the step is done"],"return_shape":"a pointer to the result","limits":{"timeout_minutes":30}}`

// FakeWarden is a WardenRunner that plays a fixed script instead of running an
// agent. It works only through the spool, as a real Warden would: it
// dispatches one Wright for a piece named after the step, waits for it to
// report, accepts it and hands back. After a restart it dispatches nothing
// new and picks up where the stored pieces left off. If the Wright did not
// report done it hands back failed.
type FakeWarden struct {
	mu       sync.Mutex
	requests []WardenRequest
	hold     <-chan struct{}
	delay    time.Duration
}

// NewFakeWarden returns a fake Warden that hands every step back.
func NewFakeWarden() *FakeWarden { return &FakeWarden{} }

// HoldUntil makes each run wait for ch to close, or the Works to stop, before
// it dispatches, so a step stays running long enough to be interrupted.
func (f *FakeWarden) HoldUntil(ch <-chan struct{}) *FakeWarden {
	f.hold = ch
	return f
}

// WithDelay makes each run wait before it dispatches.
func (f *FakeWarden) WithDelay(d time.Duration) *FakeWarden {
	f.delay = d
	return f
}

// Requests returns every Warden run started so far.
func (f *FakeWarden) Requests() []WardenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]WardenRequest(nil), f.requests...)
}

// RunWarden implements WardenRunner.
func (f *FakeWarden) RunWarden(ctx context.Context, req WardenRequest) error {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	env := spool.Env{Run: req.Env.Run, Dir: req.Env.Spool, Token: req.Env.Token}
	piece := req.Step
	call := func(id, tool string, in any) (spool.Response, error) {
		b, err := json.Marshal(in)
		if err != nil {
			return spool.Response{}, err
		}
		return spool.Call(ctx, env, id, tool, b, spool.DefaultWait)
	}
	// A refused dispatch is fine after a restart: the piece already exists.
	if resp, err := call("dispatch", "dispatch_wright", map[string]any{"piece": piece, "brief": json.RawMessage(fakeWrightBrief)}); err != nil {
		return err
	} else if !resp.OK && resp.Error.Code != spool.CodeConflict {
		return fmt.Errorf("dispatch_wright: %s", resp.Error)
	}
	var last store.Dispatch
	for {
		resp, err := call(fmt.Sprintf("inspect-%d", time.Now().UnixNano()), "inspect_wright", map[string]string{"piece": piece})
		if err != nil {
			return err
		}
		if !resp.OK {
			return fmt.Errorf("inspect_wright: %s", resp.Error)
		}
		var in Inspection
		if err := json.Unmarshal(resp.Output, &in); err != nil {
			return err
		}
		if n := len(in.Dispatches); n > 0 {
			last = in.Dispatches[n-1]
			if last.Status != store.DispatchQueued && last.Status != store.DispatchRunning {
				break
			}
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	hb := HandbackInput{Status: HandbackDone, Results: []string{"fake://" + piece}, Checked: []string{"the Wright's result for " + piece}}
	if resp, err := call("accept", "accept_piece", map[string]string{"piece": piece}); err != nil {
		return err
	} else if !resp.OK && !resultIsDone(last.Result) {
		hb = HandbackInput{Status: HandbackFailed, Checked: []string{"the Wright's result for " + piece}, Uncertainty: "the Wright did not report done"}
	}
	resp, err := call("handback", "handback", hb)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("handback: %s", resp.Error)
	}
	return nil
}
