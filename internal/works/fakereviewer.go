package works

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// FakeReviewer is a ReviewRunner that plays a script instead of running an
// agent: each review consumes the next scripted report, sent through the
// spool as a real Warden would; with none left it passes both axes. A nil
// report makes the run end without reporting.
type FakeReviewer struct {
	mu       sync.Mutex
	script   []*ReviewReport
	requests []ReviewRequest
}

// ReviewReport is what a review Warden sends through report_review.
type ReviewReport struct {
	Standards store.Axis `json:"standards"`
	Spec      store.Axis `json:"spec"`
}

// PassingReport passes both axes.
func PassingReport() *ReviewReport {
	return &ReviewReport{Standards: store.Axis{Verdict: store.VerdictPass}, Spec: store.Axis{Verdict: store.VerdictPass}}
}

// NewFakeReviewer scripts one report per review.
func NewFakeReviewer(script ...*ReviewReport) *FakeReviewer { return &FakeReviewer{script: script} }

// Requests returns every review run started so far.
func (f *FakeReviewer) Requests() []ReviewRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ReviewRequest(nil), f.requests...)
}

// RunReview implements ReviewRunner.
func (f *FakeReviewer) RunReview(ctx context.Context, req ReviewRequest) error {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	rep := PassingReport()
	if len(f.script) > 0 {
		rep, f.script = f.script[0], f.script[1:]
	}
	f.mu.Unlock()
	if rep == nil {
		return nil
	}
	in, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	resp, err := spool.Call(ctx, spool.Env{Run: req.Env.Run, Dir: req.Env.Spool, Token: req.Env.Token}, "review-"+req.ReviewID, "report_review", in, spool.DefaultWait)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("report_review: %s", resp.Error)
	}
	return nil
}
