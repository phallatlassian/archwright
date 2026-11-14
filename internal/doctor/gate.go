package doctor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// FailedError is a failing doctor report standing in the way of a run.
type FailedError struct{ Report Report }

func (e *FailedError) Error() string {
	var b strings.Builder
	b.WriteString("archwright doctor does not pass, so runs do not start until it does:")
	for _, c := range e.Report.Failures() {
		fmt.Fprintf(&b, "\n- %s: %s\n  fix: %s", c.Name, c.Detail, c.Fix)
	}
	b.WriteString("\nfix these, then run `archwright doctor` to confirm")
	return b.String()
}

// Gate lets a run start only while doctor passes. A pass is remembered for
// PassTTL and a failure for FailTTL (so a burst of runs does not repeat the
// probes); the memory is in this process only, nothing is written.
type Gate struct {
	Run     func(ctx context.Context) Report
	PassTTL time.Duration
	FailTTL time.Duration
	Now     func() time.Time

	mu   sync.Mutex
	last *Report
	at   time.Time
}

// NewGate returns a Gate over run with the default memory (10 minutes for a
// pass, 20 seconds for a failure).
func NewGate(run func(ctx context.Context) Report) *Gate {
	return &Gate{Run: run, PassTTL: 10 * time.Minute, FailTTL: 20 * time.Second, Now: time.Now}
}

// Check returns nil while doctor passes (warnings do not block) and a
// *FailedError otherwise. Concurrent callers share one doctor run.
func (g *Gate) Check(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.Now()
	if g.last != nil {
		ttl := g.FailTTL
		if g.last.OK {
			ttl = g.PassTTL
		}
		if now.Sub(g.at) < ttl {
			return verdict(*g.last)
		}
	}
	rep := g.Run(ctx)
	g.last, g.at = &rep, g.Now()
	return verdict(rep)
}

func verdict(r Report) error {
	if r.OK {
		return nil
	}
	return &FailedError{Report: r}
}
