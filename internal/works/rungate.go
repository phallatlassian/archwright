package works

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/phallatlassian/archwright/internal/gates"
	"github.com/phallatlassian/archwright/internal/store"
)

func init() {
	catalogue["run_gate"] = tool{roles: []string{RoleWright}, call: runGate}
}

// DefaultRunGateCap is how many run_gate calls one dispatch may make. It is a
// Works default, not a Guild setting (ADR 0025).
const DefaultRunGateCap = 20

// GateRan is the answer to run_gate.
type GateRan struct {
	Status    string             `json:"status"` // passed, failed or not_run
	Reason    string             `json:"reason,omitempty"`
	Results   []store.GateResult `json:"results"`
	CallsLeft int                `json:"calls_left"`
	Message   string             `json:"message"`
}

// runGate runs the Site's registry gates for a Wright (ADR 0025). The Works
// snapshots the Wright's worktree, tracked and untracked files, into a
// throwaway checkout and runs the commands there, so a gate cannot write into
// the Wright's tree. It takes at most a gate name, never a free argument.
func runGate(c *callCtx, in json.RawMessage) (any, error) {
	var args struct {
		Gate string `json:"gate"`
	}
	if err := decode(in, &args); err != nil {
		return nil, err
	}
	if args.Gate != "" {
		known := false
		for _, n := range gates.Names {
			known = known || n == args.Gate
		}
		if !known {
			return nil, fmt.Errorf("%w: gate must be one of %s, or omitted to run them all", errInvalidInput, strings.Join(gates.Names, ", "))
		}
	}
	if c.run.ctx.Job == "" || c.run.ctx.DispatchID == "" {
		return nil, fmt.Errorf("%w: this run is not a dispatched Wright", errInvalidInput)
	}
	d, err := c.st.GetDispatch(c.run.ctx.DispatchID)
	if err != nil {
		return nil, err
	}
	job, err := c.st.GetJob(c.run.ctx.Job)
	if err != nil {
		return nil, err
	}
	if job.Outcome != "" {
		return nil, fmt.Errorf("job %q is %s: no gates are run for it: %w", job.ID, job.Outcome, store.ErrConflict)
	}
	site, err := c.runs.gateSite(job.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidInput, err)
	}
	r := c.runs
	r.mu.Lock()
	sm, timeout, wt := r.sites, r.gateTimeout, r.wrightWts[d.ID]
	r.mu.Unlock()
	if sm == nil {
		return nil, fmt.Errorf("%w: no Site manager is configured, so there is nowhere to run gates", errInvalidInput)
	}
	if isMergeBasePiece(d.Piece) { // a merge Wright works in the integration worktree
		if wt, err = sm.IntegrationWorktree(job.ID); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(wt); wt == "" || err != nil {
		return nil, fmt.Errorf("%w: this Wright has no worktree to run gates on", errInvalidInput)
	}
	left, release, err := r.takeGateSlot(job.ID, d.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	co, err := sm.SnapshotCheckout(job.ID, wt)
	if err != nil {
		return nil, fmt.Errorf("taking a snapshot of your worktree: %w", err)
	}
	results := gates.RunNamed(r.bg, co.Dir, site.Gates, timeout, args.Gate)
	if rerr := co.Release(); rerr != nil {
		log.Printf("run_gate: removing the snapshot: %v", rerr)
	}
	if r.bg.Err() != nil {
		return nil, fmt.Errorf("the Works stopped before the gates finished: %w", store.ErrConflict)
	}
	status, reason := gates.Summarize(results)
	return GateRan{
		Status: status, Reason: reason, Results: results, CallsLeft: left,
		Message: "This is your early look at a snapshot of your worktree. The Works runs the authoritative gates again at handback.",
	}, nil
}

// takeGateSlot admits one run_gate call: one at a time per Job, and at most
// DefaultRunGateCap per dispatch. A refused call does not count.
func (r *Runs) takeGateSlot(jobID, dispatchID string) (left int, release func(), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gateBusy == nil {
		r.gateBusy, r.gateCalls = map[string]bool{}, map[string]int{}
	}
	if r.gateBusy[jobID] {
		return 0, nil, fmt.Errorf("another run_gate is running for this Job; wait for it and call again: %w", store.ErrConflict)
	}
	if r.gateCalls[dispatchID] >= DefaultRunGateCap {
		return 0, nil, fmt.Errorf("run_gate is capped at %d calls per dispatch and you have used them; report your status: %w", DefaultRunGateCap, store.ErrConflict)
	}
	r.gateBusy[jobID] = true
	r.gateCalls[dispatchID]++
	return DefaultRunGateCap - r.gateCalls[dispatchID], func() {
		r.mu.Lock()
		delete(r.gateBusy, jobID)
		r.mu.Unlock()
	}, nil
}
