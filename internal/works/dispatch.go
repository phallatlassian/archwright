package works

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/store"
)

// DefaultReworkRounds is how many times a piece is sent back before it fails (ADR 0006).
const DefaultReworkRounds = 2

// Guild is what the Works needs of a Guild definition to dispatch for it.
type Guild struct {
	Name         string
	Standards    string       // injected into every brief
	ReworkRounds int          // zero means DefaultReworkRounds
	Binding      defs.Binding // the permissions its runs get; never wider than the developer's runner configuration
}

func (g Guild) reworkLimit() int {
	if g.ReworkRounds <= 0 {
		return DefaultReworkRounds
	}
	return g.ReworkRounds
}

// GuildSource looks a Guild up by name; an unknown Guild is an error.
type GuildSource func(name string) (Guild, error)

// GuildsFromRegistry serves the loaded Guild definitions. Each lookup binds
// the Guild to its default runner, so a requirement the runner cannot enforce
// fails the lookup (ADR 0009).
func GuildsFromRegistry(reg *defs.Registry, profiles map[string]defs.Profile) GuildSource {
	return func(name string) (Guild, error) {
		g, ok := reg.Guild(name)
		if !ok {
			return Guild{}, fmt.Errorf("%w: no guild %q", errInvalidInput, name)
		}
		b, err := defs.Bind(g, "", profiles)
		if err != nil {
			return Guild{}, fmt.Errorf("%w: %v", errInvalidInput, err)
		}
		return Guild{Name: g.Name, Standards: g.Charter, ReworkRounds: g.ReworkRounds, Binding: b}, nil
	}
}

// RunRequest is what a Runner is given to carry out one dispatch.
type RunRequest struct {
	Env        RunEnv // the Wright's run: it reports through the spool in Env
	Job        string
	Guild      string // the Guild whose binding picks the runner
	Piece      string
	DispatchID string
	Worktree   string            // the Wright's own worktree; empty when the Works has no Sites
	Brief      string            // the rendered brief, Guild standards included
	ExtraEnv   map[string]string // further variables for the runner process, such as GIT_CONFIG_GLOBAL for delivered skills
	Trace      Trace             // where a runner reports its timeline and usage for the dashboard
}

// Trace is how a run shows itself on the dashboard (ADR 0007): timeline
// entries, each with the vendor event behind it kept as raw run content
// (ADR 0017), and what the run consumed. A nil Trace, or either func nil, is a no-op.
type Trace struct {
	Event func(kind, summary string, raw json.RawMessage)
	Usage func(runner string, inputTokens, outputTokens int, costUSD float64)
}

// Emit adds a timeline entry.
func (t Trace) Emit(kind, summary string, raw json.RawMessage) {
	if t.Event != nil {
		t.Event(kind, summary, raw)
	}
}

// Consumed reports what the run used and which runner ran it.
func (t Trace) Consumed(runner string, inputTokens, outputTokens int, costUSD float64) {
	if t.Usage != nil {
		t.Usage(runner, inputTokens, outputTokens, costUSD)
	}
}

// Runner carries out one Wright run (ADR 0003). Real adapters spawn a vendor
// CLI; the fake runner plays a script. An error, or returning without a
// report, fails the dispatch.
type Runner interface {
	Run(ctx context.Context, req RunRequest) error
}

// dispatchTools are the Warden's and Wright's tools, plus the Archwright's
// way to read what they produced (ADR 0004, 0006, 0014).
var dispatchTools = map[string]tool{
	"dispatch_wright": {roles: []string{RoleWarden}, call: dispatchWright},
	"rework_wright":   {roles: []string{RoleWarden}, call: reworkWright},
	"inspect_wright":  {roles: []string{RoleWarden}, call: inspectWright},
	"accept_piece":    {roles: []string{RoleWarden}, call: acceptPiece},
	"handback":        {roles: []string{RoleWarden}, call: handback},
	"report_result":   {roles: []string{RoleWright}, call: reportResult},
	"show_queue":      {roles: []string{RoleArchwright}, call: showQueue},
	"show_needs_you":  {roles: []string{RoleArchwright}, call: showNeedsYou},
	"show_dispatches": {roles: []string{RoleArchwright}, call: showDispatches},
	"show_handback":   {roles: []string{RoleArchwright}, call: showHandback},
	"list_flags":      {roles: []string{RoleArchwright}, call: listFlags},
}

func init() {
	for name, t := range dispatchTools {
		catalogue[name] = t
	}
}

// warden checks the calling run is a Warden bound to a Job and Guild and returns its Guild.
func (c *callCtx) warden() (Guild, error) {
	if c.run.ctx.Job == "" || c.run.ctx.Guild == "" {
		return Guild{}, fmt.Errorf("%w: this run is not bound to a Job and Guild", errInvalidInput)
	}
	if c.run.ctx.Review != "" {
		return Guild{}, fmt.Errorf("%w: a Review run only reports through report_review", errInvalidInput)
	}
	job, err := c.st.GetJob(c.run.ctx.Job)
	if err != nil {
		return Guild{}, err
	}
	if job.ParkedFor != "" {
		return Guild{}, fmt.Errorf("job %q is parked for the Archwright: %w", job.ID, store.ErrConflict)
	}
	c.runs.mu.Lock()
	src := c.runs.guilds
	c.runs.mu.Unlock()
	if src == nil {
		return Guild{}, fmt.Errorf("%w: no guilds are configured", errInvalidInput)
	}
	return src(c.run.ctx.Guild)
}

// planCleared refuses work for a Job whose Plan awaits, or was refused, the
// developer's go. A Jig Plan is never held; a Job with no Plan is not either.
func (c *callCtx) planCleared() error {
	p, err := c.st.GetPlan(c.run.ctx.Job)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !p.Cleared() {
		return fmt.Errorf("the Plan of job %q is %s: the developer must approve an ad hoc Plan before work is dispatched: %w", c.run.ctx.Job, p.Approval, store.ErrConflict)
	}
	return nil
}

// jobRunnable refuses work for a Job the developer paused or that is closed,
// including one they cancelled.
func (c *callCtx) jobRunnable() error {
	job, err := c.st.GetJob(c.run.ctx.Job)
	if err != nil {
		return err
	}
	switch {
	case job.Outcome != "":
		return fmt.Errorf("job %q is %s: no more work is dispatched for it: %w", job.ID, job.Outcome, store.ErrConflict)
	case job.PausedAt != "":
		return fmt.Errorf("job %q is paused by the developer: no work is dispatched until it is resumed: %w", job.ID, store.ErrConflict)
	}
	return nil
}

// pieceInJob loads a piece of the calling Warden's Job, whatever its Guild.
// Only reads use it: a step's brief names earlier steps' pieces, which a
// Warden of another Guild must be able to inspect.
func (c *callCtx) pieceInJob(name string) (store.Piece, error) {
	if err := requireField("piece", name); err != nil {
		return store.Piece{}, err
	}
	return c.st.GetPiece(c.run.ctx.Job, name)
}

// pieceOf loads a piece the calling Warden may change: its own Job and Guild.
func (c *callCtx) pieceOf(name string) (store.Piece, error) {
	p, err := c.pieceInJob(name)
	if err != nil {
		return p, err
	}
	if p.Guild != c.run.ctx.Guild {
		return store.Piece{}, fmt.Errorf("piece %q: %w", name, store.ErrNotFound)
	}
	return p, nil
}

// DispatchInfo is the answer to dispatch_wright and rework_wright.
type DispatchInfo struct {
	DispatchID string `json:"dispatch_id,omitempty"`
	Piece      string `json:"piece"`
	Round      int    `json:"round"`
	Status     string `json:"status"`                   // running, queued behind the caps, or failed when the piece is out of rework rounds
	Reason     string `json:"reason,omitempty"`         // set when the piece failed
	QueuePos   int    `json:"queue_position,omitempty"` // set when queued: 1 is next
}

// launch warns of any scope overlap with other Jobs on the Site, then hands
// the dispatch to the scheduler.
func (c *callCtx) launch(d store.Dispatch, b Brief) (DispatchInfo, error) {
	c.runs.warnScopeOverlap(d, b)
	pos, err := c.runs.launch(d)
	if err != nil {
		return DispatchInfo{}, err
	}
	info := DispatchInfo{DispatchID: d.ID, Piece: d.Piece, Round: d.Round, Status: store.DispatchRunning}
	if pos > 0 {
		info.Status, info.QueuePos = store.DispatchQueued, pos
	}
	return info, nil
}

func dispatchWright(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Piece string          `json:"piece"`
		Brief json.RawMessage `json:"brief"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if err := requireField("piece", a.Piece); err != nil {
		return nil, err
	}
	if isMergeWright(a.Piece) {
		return nil, fmt.Errorf("%w: the %q and %q piece prefixes are reserved for merge Wrights", errInvalidInput, mergeBasePiece+"-", mergePiecePiece+"-")
	}
	g, err := c.warden()
	if err != nil {
		return nil, err
	}
	if err := c.planCleared(); err != nil {
		return nil, err
	}
	if err := c.jobRunnable(); err != nil {
		return nil, err
	}
	b, err := parseBrief(a.Brief)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(b)
	d, err := c.st.NewPiece(c.run.ctx.Job, g.Name, a.Piece, c.run.id, raw, b.Render(g.Name, g.Standards, 0, ""))
	if err != nil {
		return nil, err
	}
	return c.launch(d, b)
}

func reworkWright(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Piece     string          `json:"piece"`
		Reason    string          `json:"reason"`    // what was wrong with the last result
		Diagnosis string          `json:"diagnosis"` // the Warden's diagnosis, kept if the piece fails
		Brief     json.RawMessage `json:"brief"`     // the amended brief, complete
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if err := requireField("reason", a.Reason); err != nil {
		return nil, err
	}
	if isMergeWright(a.Piece) {
		return nil, fmt.Errorf("%w: merge Wright pieces cannot be reworked; call merge_base or accept_piece again", errInvalidInput)
	}
	g, err := c.warden()
	if err != nil {
		return nil, err
	}
	if err := c.jobRunnable(); err != nil {
		return nil, err
	}
	piece, err := c.pieceOf(a.Piece)
	if err != nil {
		return nil, err
	}
	b, err := parseBrief(a.Brief)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(b)
	asks, err := c.st.PieceAsks(c.run.ctx.Job, a.Piece)
	if err != nil {
		return nil, err
	}
	p, d, err := c.st.Rework(c.run.ctx.Job, a.Piece, g.reworkLimit(), a.Reason, a.Diagnosis, c.run.id, raw, b.Render(g.Name, g.Standards, piece.Reworks+1, a.Reason)+qaSection(asks))
	if err != nil {
		return nil, err
	}
	if d == nil { // out of rounds: the piece fails
		return DispatchInfo{Piece: p.Piece, Round: p.Reworks, Status: store.PieceFailed, Reason: p.Reason}, nil
	}
	return c.launch(*d, b)
}

// Inspection is what a Warden sees of a piece: never any flags.
type Inspection struct {
	Piece      store.Piece      `json:"piece"`
	Dispatches []store.Dispatch `json:"dispatches"`
}

// maxWaitSeconds caps inspect_wright's blocking wait below the spool client's
// default patience (spool.DefaultWait), so the call always answers in time.
const maxWaitSeconds = 45

// waitPoll is how often a blocking inspect_wright re-reads the dispatches.
var waitPoll = 250 * time.Millisecond

func inspectWright(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Piece       string `json:"piece"`
		WaitSeconds int    `json:"wait_seconds"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if _, err := c.warden(); err != nil {
		return nil, err
	}
	p, err := c.pieceInJob(a.Piece)
	if err != nil {
		return nil, err
	}
	if a.WaitSeconds < 0 || a.WaitSeconds > maxWaitSeconds {
		return nil, fmt.Errorf("%w: wait_seconds must be between 0 and %d", errInvalidInput, maxWaitSeconds)
	}
	deadline := time.NewTimer(time.Duration(a.WaitSeconds) * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(waitPoll)
	defer tick.Stop()
	for {
		out, err := inspection(c.st, p)
		if err != nil || a.WaitSeconds == 0 || !wrightWorking(out) {
			return out, err
		}
		select { // block here, in the foreground, so the Warden never has to sleep in the background
		case <-deadline.C:
			return out, nil
		case <-c.run.ending:
			return out, nil
		case <-tick.C:
		}
		if p, err = c.pieceInJob(a.Piece); err != nil {
			return nil, err
		}
	}
}

func inspection(st *store.Store, p store.Piece) (Inspection, error) {
	all, err := st.Dispatches(p.JobID)
	out := Inspection{Piece: p, Dispatches: []store.Dispatch{}}
	for _, d := range all {
		if d.Piece == p.Piece {
			out.Dispatches = append(out.Dispatches, d)
		}
	}
	return out, err
}

// wrightWorking reports whether the piece's latest dispatch is still queued or running.
func wrightWorking(in Inspection) bool {
	if len(in.Dispatches) == 0 {
		return false
	}
	st := in.Dispatches[len(in.Dispatches)-1].Status
	return st == store.DispatchQueued || st == store.DispatchRunning
}

// handbackSummary is the line the Archwright's inbox shows for a step's
// handback. The open-pr step's carries the Job's pull request, so the
// Archwright can give the developer the link without another call.
func handbackSummary(job store.Job, step, status, guild string) string {
	s := fmt.Sprintf("Step %s handed back %s by the %s Warden", step, status, guild)
	if step == OpenPRStep && job.PR != "" {
		s += ". Pull request: " + job.PR
	}
	return s
}

func handback(c *callCtx, in json.RawMessage) (any, error) {
	var a HandbackInput
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if _, err := c.warden(); err != nil {
		return nil, err
	}
	if step := c.run.ctx.Task; step != "" { // before anything is saved: a step that is not running takes no handback
		if sr, err := c.st.GetStepRun(c.run.ctx.Job, step); err != nil {
			return nil, err
		} else if sr.Status != store.StepRunning {
			return nil, fmt.Errorf("step %q is %s, not running: %w", step, sr.Status, store.ErrConflict)
		}
	}
	pieces, err := c.st.Pieces(c.run.ctx.Job)
	if err != nil {
		return nil, err
	}
	mine := pieces[:0:0]
	for _, p := range pieces {
		if p.Guild == c.run.ctx.Guild {
			mine = append(mine, p)
		}
	}
	for _, p := range mine {
		if d, err := c.st.LatestDispatch(p.JobID, p.Piece); err == nil && (d.Status == store.DispatchRunning || d.Status == store.DispatchQueued || d.Status == store.DispatchBlocked) {
			return nil, fmt.Errorf("piece %q still has a Wright running or blocked on an ask: %w", p.Piece, store.ErrConflict)
		}
	}
	if err := a.Validate(mine); err != nil {
		return nil, err
	}
	body, err := json.Marshal(HandbackBody{Status: a.Status, Results: a.Results, Checked: a.Checked, Rejected: a.Rejected, Uncertainty: a.Uncertainty, Pieces: mine})
	if err != nil {
		return nil, err
	}
	flags := make([]store.NewFlag, len(a.Flags))
	for i, f := range a.Flags {
		flags[i] = store.NewFlag{Kind: f.Kind, Description: f.Description, Evidence: f.Evidence}
	}
	park := ""
	if a.Status == HandbackPartial { // accepted pieces are kept; the Archwright decides the rest
		park = "partial handback: the Archwright decides what to do with the pieces that did not land"
	}
	h, err := c.st.SaveHandback(c.run.ctx.Job, a.Status, body, flags, park)
	if err != nil {
		return nil, err
	}
	c.runs.startGates(c.run.ctx.Job, h.ID)
	if step := c.run.ctx.Task; step != "" {
		job, err := c.st.GetJob(c.run.ctx.Job)
		if err != nil {
			return nil, err
		}
		summary := handbackSummary(job, step, h.Status, c.run.ctx.Guild)
		if err := c.st.HandBackStep(c.run.ctx.Job, step, h.ID, h.Status, summary, json.RawMessage(body)); err != nil {
			return nil, err
		}
	}
	// The Warden is told how many flags were taken, not shown them back.
	return map[string]any{"handback_id": h.ID, "status": h.Status, "flags_recorded": len(flags), "parked": park != "", "gates": store.GateRunRunning}, nil
}

func reportResult(c *callCtx, in json.RawMessage) (any, error) {
	var a WrightResult
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if c.run.ctx.DispatchID == "" {
		return nil, fmt.Errorf("%w: this run carries out no dispatch", errInvalidInput)
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	d, err := c.st.GetDispatch(c.run.ctx.DispatchID)
	if err != nil {
		return nil, err
	}
	c.runs.mu.Lock()
	wt := c.runs.wrightWts[d.ID]
	c.runs.mu.Unlock()
	c.runs.commitWright(d, wt, a, c.runs.traceFor(c.run.id)) // before the dispatch reads as reported, so the Warden never acts on uncommitted work
	raw, _ := json.Marshal(a)
	if err := c.st.RecordResult(c.run.ctx.DispatchID, raw); err != nil {
		return nil, err
	}
	return map[string]string{"status": "recorded"}, nil
}

func jobArg(in json.RawMessage) (string, error) {
	var a struct {
		JobID string `json:"job_id"`
	}
	if err := decode(in, &a); err != nil {
		return "", err
	}
	return a.JobID, requireField("job_id", a.JobID)
}

func showDispatches(c *callCtx, in json.RawMessage) (any, error) {
	id, err := jobArg(in)
	if err != nil {
		return nil, err
	}
	if _, err := c.st.GetJob(id); err != nil {
		return nil, err
	}
	return c.st.Dispatches(id)
}

func showQueue(c *callCtx, in json.RawMessage) (any, error) {
	if err := decode(in, &struct{}{}); err != nil {
		return nil, err
	}
	return c.runs.Queue(), nil
}

func showNeedsYou(c *callCtx, in json.RawMessage) (any, error) {
	if err := decode(in, &struct{}{}); err != nil {
		return nil, err
	}
	return c.st.NeedsYou()
}

func showHandback(c *callCtx, in json.RawMessage) (any, error) {
	id, err := jobArg(in)
	if err != nil {
		return nil, err
	}
	h, err := c.st.LatestHandback(id)
	if err != nil {
		return nil, err
	}
	flags, err := c.st.Flags(id)
	if err != nil {
		return nil, err
	}
	var gates *store.GateRun // absent for a handback saved before gates were recorded
	if g, err := c.st.GateRunFor(h.ID); err == nil {
		gates = &g
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return struct {
		store.Handback
		Flags []store.Flag   `json:"flags"`
		Gates *store.GateRun `json:"gates,omitempty"` // the Works' own re-run, trusted over any Warden claim
	}{h, flags, gates}, nil
}

func listFlags(c *callCtx, in json.RawMessage) (any, error) {
	id, err := jobArg(in)
	if err != nil {
		return nil, err
	}
	if _, err := c.st.GetJob(id); err != nil {
		return nil, err
	}
	return c.st.Flags(id)
}

// launch hands a recorded dispatch to the scheduler, which starts its Wright
// run on the configured Runner once the caps allow, and queues it until then.
// It returns the dispatch's place in the queue (zero if it started at once),
// or an error if the run started at once and could not be launched; a
// dispatch that never reported then fails.
func (r *Runs) launch(d store.Dispatch) (queuePosition int, err error) { return r.sched.submit(d) }

// failDispatch records a failed result for a dispatch that has not reported.
func (r *Runs) failDispatch(id, msg string) {
	raw, _ := json.Marshal(WrightResult{Status: ResultFailed, Uncertainty: msg})
	if err := r.st.RecordResult(id, raw); err != nil && !errors.Is(err, store.ErrConflict) {
		log.Printf("dispatch %s: recording failure: %v", id, err)
	}
}

// start launches the Wright run for a dispatch the scheduler has released.
// The run ends when the Runner returns: a dispatch that never reported then
// fails, unless the Runner was rate-limited, which requeues it.
func (r *Runs) start(d store.Dispatch) error {
	r.mu.Lock()
	runner, closed := r.runner, r.bg.Err() != nil
	if !closed {
		r.wg.Add(1) // under mu, so Close cannot Wait between the check and the Add
	}
	r.mu.Unlock()
	if closed {
		return fmt.Errorf("%w: the Works is shutting down", errInvalidInput)
	}
	if runner == nil {
		r.wg.Done()
		return fmt.Errorf("%w: no runner is configured", errInvalidInput)
	}
	root, err := r.spoolRootFor(d.Guild)
	if err != nil {
		r.wg.Done()
		return err
	}
	env, err := r.StartFor(RoleWright, root, RunContext{Job: d.JobID, Guild: d.Guild, DispatchID: d.ID})
	if err != nil {
		r.wg.Done()
		return err
	}
	if err := r.st.AttachRun(d.ID, env.Run); err != nil {
		r.End(env.Run)
		r.wg.Done()
		return err
	}
	if err := r.st.StartRunRecord(env.Run, d.JobID, d.ID, d.Guild, "", "", d.WardenRun); err != nil {
		log.Printf("run %s: recording it for the dashboard: %v", env.Run, err)
	}
	last := &lastWords{}
	trace := last.wrap(r.traceFor(env.Run))
	// Each run has its own context so the developer can cancel one Wright.
	ctx, cancel := context.WithCancel(r.bg)
	r.mu.Lock()
	r.cancels[d.ID] = cancel
	if r.cancelled[d.ID] {
		cancel() // cancelled between leaving the queue and starting
	}
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		defer cancel()
		wt, from, runErr := r.wrightWorktree(d)
		brief := d.Rendered
		if runErr == nil && d.Round > 0 && !isMergeWright(d.Piece) && wt != "" {
			brief += startingPoint(from)
		}
		var extra map[string]string
		if runErr == nil {
			extra, runErr = r.deliverSkillsFor(d, env.Run, wt)
		}
		if runErr == nil {
			runErr = runner.Run(ctx, RunRequest{Env: env, Job: d.JobID, Guild: d.Guild, Piece: d.Piece, DispatchID: d.ID, Worktree: wt, Brief: brief, ExtraEnv: extra, Trace: trace})
		}
		unreported := false
		cur, curErr := r.st.GetDispatch(d.ID)
		if curErr == nil && cur.Status == store.DispatchRunning {
			unreported = true
		}
		if _, limited := asRateLimit(runErr); unreported && !limited {
			r.commitWright(d, wt, WrightResult{Status: ResultFailed}, trace) // a run that never reported has failed; a requeued run carries on in the same worktree
		}
		r.End(env.Run)
		r.mu.Lock()
		wasCancelled := r.cancelled[d.ID]
		if _, limited := asRateLimit(runErr); !limited || !unreported {
			delete(r.wrightWts, d.ID) // only a requeued dispatch runs in it again
		}
		delete(r.cancels, d.ID)
		delete(r.cancelled, d.ID)
		r.mu.Unlock()
		ended := "done"
		switch {
		case wasCancelled && unreported:
			ended = "cancelled"
		case unreported || runErr != nil:
			ended = "failed"
		}
		if err := r.st.EndRunRecord(env.Run, ended); err != nil {
			log.Printf("run %s: recording its end: %v", env.Run, err)
		}
		if wasCancelled && unreported {
			r.failDispatch(d.ID, cancelledByDeveloper)
			r.sched.finished(d.ID, false)
			return
		}
		if rl, ok := asRateLimit(runErr); ok && unreported {
			r.sched.rateLimited(d.ID, rl)
			return
		}
		if unreported {
			msg := "the run ended without reporting a result"
			if runErr != nil {
				msg = "the run failed: " + runErr.Error()
			} else if t := last.text(); t != "" {
				msg += "; its last message was:\n" + clipRunes(t, lastWordsMax) // a Wright that wrote its findings but never reported them
			}
			r.failDispatch(d.ID, msg)
		}
		r.sched.finished(d.ID, runErr == nil)
	}()
	return nil
}

// lastWordsMax caps how much of a Wright's last message a failure keeps.
const lastWordsMax = 8000

// lastWords remembers a run's last message, so a Wright that ends without
// calling report_result does not lose what it wrote (a read-only reviewer's
// findings, say): the failure carries it to the Warden.
type lastWords struct {
	mu  sync.Mutex
	msg string
}

func (l *lastWords) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.msg
}

// wrap returns t that also notes each message and final result the runner reports.
func (l *lastWords) wrap(t Trace) Trace {
	next := t.Event
	t.Event = func(kind, summary string, raw json.RawMessage) {
		if kind == "message" || kind == "result" {
			if m := eventText(summary, raw); m != "" {
				l.mu.Lock()
				l.msg = m
				l.mu.Unlock()
			}
		}
		if next != nil {
			next(kind, summary, raw)
		}
	}
	return t
}

// eventText is the full text of a message or result event: the vendor's own
// result string, or its text blocks, falling back to the one-line summary.
func eventText(summary string, raw json.RawMessage) string {
	var ev struct {
		Result  string `json:"result"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &ev) == nil {
		if strings.TrimSpace(ev.Result) != "" {
			return ev.Result
		}
		var parts []string
		for _, b := range ev.Message.Content {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n\n")
		}
	}
	return summary
}

// clipRunes cuts s to at most n runes, saying so.
func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "\n[cut: the message was longer]"
	}
	return s
}

// traceFor is where a run's timeline and usage are recorded for the dashboard.
func (r *Runs) traceFor(run string) Trace {
	return Trace{
		Event: func(kind, summary string, raw json.RawMessage) {
			if _, err := r.st.AddRunEvent(run, kind, summary, raw); err != nil {
				log.Printf("run %s: recording an event: %v", run, err)
			}
		},
		Usage: func(runner string, in, out int, cost float64) {
			if err := r.st.SetRunUsage(run, runner, in, out, cost); err != nil {
				log.Printf("run %s: recording usage: %v", run, err)
			}
		},
	}
}

// commitWright commits what a Wright left in its worktree to its piece branch
// when it reports, or when its run ends without reporting (ADR 0025).
// The Wright never runs git add or commit. A failure is recorded on the run
// and does not fail the dispatch: the handback gates run on what was committed.
func (r *Runs) commitWright(d store.Dispatch, wt string, res WrightResult, trace Trace) {
	r.mu.Lock()
	sm := r.sites
	r.mu.Unlock()
	if sm == nil || wt == "" || isMergeWright(d.Piece) { // a merge Wright commits for itself
		return
	}
	status := res.Status
	msg := fmt.Sprintf("%s (round %d)", d.Piece, d.Round)
	if status == ResultDone {
		msg = "piece " + msg
	} else {
		msg = "wip: piece " + msg + ", " + status
	}
	if res.Result != "" {
		msg += "\n\nResult: " + res.Result
	}
	sha, err := sm.CommitWorktree(d.JobID, wt, msg)
	switch {
	case err != nil:
		log.Printf("dispatch %s: committing the worktree: %v", d.ID, err)
		trace.Emit("commit", "committing the worktree failed: "+err.Error(), commitRaw("failed", ""))
	case sha == "":
		trace.Emit("commit", "no changes to commit", commitRaw("unchanged", ""))
	default:
		trace.Emit("commit", "committed "+sha, commitRaw("committed", sha))
	}
}

func commitRaw(outcome, sha string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"type": "commit", "outcome": outcome, "sha": sha})
	return b
}

// wrightWorktree gives a dispatch's Wright its own worktree on its own branch
// (ADR 0008). Without Sites there is none. A later round of a piece continues
// from the branch of its previous round, whose work the Works committed when
// that round reported (ADR 0025); from names that branch, or is empty when the
// worktree starts from the Job's integration branch.
func (r *Runs) wrightWorktree(d store.Dispatch) (path, from string, err error) {
	r.mu.Lock()
	sm, kept := r.sites, r.wrightWts[d.ID]
	r.mu.Unlock()
	if sm == nil {
		return "", "", nil
	}
	if _, err := os.Stat(kept); kept != "" && err == nil {
		return kept, "", nil // requeued after a rate limit: the same worktree, not another
	}
	if isMergeWright(d.Piece) { // a merge Wright commits on the integration branch itself, so the push is a fast-forward (ADR 0022)
		path, err := sm.IntegrationWorktree(d.JobID)
		if err != nil {
			return "", "", fmt.Errorf("finding the integration worktree: %w", err)
		}
		return path, "", nil
	}
	if err := sm.EnsureIntegration(d.JobID); err != nil {
		return "", "", fmt.Errorf("creating the integration worktree: %w", err)
	}
	from = r.previousBranch(d)
	wt, err := sm.CreateWorktree(d.JobID, store.KindWright, from)
	if err != nil && from != "" {
		log.Printf("dispatch %s: continuing from %s: %v; starting from the integration branch", d.ID, from, err)
		from = ""
		wt, err = sm.CreateWorktree(d.JobID, store.KindWright, "")
	}
	if err != nil {
		return "", "", fmt.Errorf("creating the Wright's worktree: %w", err)
	}
	r.mu.Lock()
	r.wrightWts[d.ID] = wt.Path
	r.mu.Unlock()
	if err := r.st.SetDispatchBranch(d.ID, wt.Branch); err != nil { // accept_piece merges this branch
		return "", "", fmt.Errorf("recording the Wright's branch: %w", err)
	}
	return wt.Path, from, nil
}

// previousBranch is the branch of the piece's latest earlier round that had a
// worktree, or "" for a first round.
func (r *Runs) previousBranch(d store.Dispatch) string {
	if d.Round == 0 {
		return ""
	}
	ds, err := r.st.Dispatches(d.JobID)
	if err != nil {
		log.Printf("dispatch %s: finding the previous round: %v", d.ID, err)
		return ""
	}
	branch, round := "", -1
	for _, p := range ds {
		if p.Piece == d.Piece && p.Round < d.Round && p.Round > round && p.Branch != "" {
			branch, round = p.Branch, p.Round
		}
	}
	return branch
}

// startingPoint tells a later round's Wright what its worktree holds: the
// previous round's committed work, or a fresh tree when none could be used.
func startingPoint(from string) string {
	if from == "" {
		return "\n## Starting point\n\nYour worktree is fresh from the Job's integration branch. The previous round's work is not in it.\n"
	}
	return "\n## Starting point\n\nYour worktree continues from the previous round's work, not a fresh tree: the Works committed whatever that round left, finished or not, to branch `" + from + "`, and your branch starts there. " +
		"Look at what is there (`git log`, `git show`) and build on it instead of redoing it.\n"
}
