package works

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

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
	Piece      string
	DispatchID string
	Brief      string            // the rendered brief, Guild standards included
	ExtraEnv   map[string]string // further variables for the runner process, such as GIT_CONFIG_GLOBAL for delivered skills
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

// pieceOf loads a piece the calling Warden may touch: its own Job and Guild.
func (c *callCtx) pieceOf(name string) (store.Piece, error) {
	if err := requireField("piece", name); err != nil {
		return store.Piece{}, err
	}
	p, err := c.st.GetPiece(c.run.ctx.Job, name)
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
	d, err := c.st.NewPiece(c.run.ctx.Job, g.Name, a.Piece, raw, b.Render(g.Name, g.Standards, 0, ""))
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
	p, d, err := c.st.Rework(c.run.ctx.Job, a.Piece, g.reworkLimit(), a.Reason, a.Diagnosis, raw, b.Render(g.Name, g.Standards, piece.Reworks+1, a.Reason)+qaSection(asks))
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

func inspectWright(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Piece string `json:"piece"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if _, err := c.warden(); err != nil {
		return nil, err
	}
	p, err := c.pieceOf(a.Piece)
	if err != nil {
		return nil, err
	}
	all, err := c.st.Dispatches(p.JobID)
	if err != nil {
		return nil, err
	}
	out := Inspection{Piece: p, Dispatches: []store.Dispatch{}}
	for _, d := range all {
		if d.Piece == p.Piece {
			out.Dispatches = append(out.Dispatches, d)
		}
	}
	return out, nil
}

func acceptPiece(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Piece string `json:"piece"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if _, err := c.warden(); err != nil {
		return nil, err
	}
	if _, err := c.pieceOf(a.Piece); err != nil {
		return nil, err
	}
	if err := c.st.AcceptPiece(c.run.ctx.Job, a.Piece, resultIsDone); err != nil {
		return nil, err
	}
	return c.st.GetPiece(c.run.ctx.Job, a.Piece)
}

func handback(c *callCtx, in json.RawMessage) (any, error) {
	var a HandbackInput
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if _, err := c.warden(); err != nil {
		return nil, err
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
	runner, root, closed := r.runner, r.spoolRoot, r.bg.Err() != nil
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
		extra, runErr := r.deliverSkillsFor(d)
		if runErr == nil {
			runErr = runner.Run(ctx, RunRequest{Env: env, Job: d.JobID, Piece: d.Piece, DispatchID: d.ID, Brief: d.Rendered, ExtraEnv: extra})
		}
		unreported := false
		if cur, err := r.st.GetDispatch(d.ID); err == nil && cur.Status == store.DispatchRunning {
			unreported = true
		}
		r.End(env.Run)
		r.mu.Lock()
		wasCancelled := r.cancelled[d.ID]
		delete(r.cancels, d.ID)
		delete(r.cancelled, d.ID)
		r.mu.Unlock()
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
			}
			r.failDispatch(d.ID, msg)
		}
		r.sched.finished(d.ID, runErr == nil)
	}()
	return nil
}
