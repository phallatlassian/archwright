package works

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// DefaultReworkRounds is how many times a piece is sent back before it fails (ADR 0006).
const DefaultReworkRounds = 2

// Guild is what the Works needs of a Guild definition to dispatch for it.
type Guild struct {
	Name         string
	Standards    string // injected into every brief
	ReworkRounds int    // zero means DefaultReworkRounds
}

func (g Guild) reworkLimit() int {
	if g.ReworkRounds <= 0 {
		return DefaultReworkRounds
	}
	return g.ReworkRounds
}

// GuildSource looks a Guild up by name; an unknown Guild is an error.
type GuildSource func(name string) (Guild, error)

// GuildsFromDir reads Guilds from dir/<name>/standards.md (ADR 0018's config home).
func GuildsFromDir(dir string) GuildSource {
	return func(name string) (Guild, error) {
		if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			return Guild{}, fmt.Errorf("%w: no guild %q", errInvalidInput, name)
		}
		gdir := filepath.Join(dir, name)
		if fi, err := os.Stat(gdir); err != nil || !fi.IsDir() {
			return Guild{}, fmt.Errorf("%w: no guild %q", errInvalidInput, name)
		}
		b, err := os.ReadFile(filepath.Join(gdir, "standards.md"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Guild{}, err
		}
		return Guild{Name: name, Standards: string(b)}, nil
	}
}

// RunRequest is what a Runner is given to carry out one dispatch.
type RunRequest struct {
	Env        RunEnv // the Wright's run: it reports through the spool in Env
	Job        string
	Piece      string
	DispatchID string
	Brief      string // the rendered brief, Guild standards included
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
	c.runs.mu.Lock()
	src := c.runs.guilds
	c.runs.mu.Unlock()
	if src == nil {
		return Guild{}, fmt.Errorf("%w: no guilds are configured", errInvalidInput)
	}
	return src(c.run.ctx.Guild)
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
	Status     string `json:"status"`           // running, or failed when the piece is out of rework rounds
	Reason     string `json:"reason,omitempty"` // set when the piece failed
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
	b, err := parseBrief(a.Brief)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(b)
	d, err := c.st.NewPiece(c.run.ctx.Job, g.Name, a.Piece, raw, b.Render(g.Name, g.Standards, 0, ""))
	if err != nil {
		return nil, err
	}
	if err := c.runs.launch(d); err != nil {
		return nil, err
	}
	return DispatchInfo{DispatchID: d.ID, Piece: d.Piece, Round: d.Round, Status: store.DispatchRunning}, nil
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
	if _, err := c.pieceOf(a.Piece); err != nil {
		return nil, err
	}
	b, err := parseBrief(a.Brief)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(b)
	p, d, err := c.st.Rework(c.run.ctx.Job, a.Piece, g.reworkLimit(), a.Reason, a.Diagnosis, raw, b.Render(g.Name, g.Standards, p0(c, a.Piece)+1, a.Reason))
	if err != nil {
		return nil, err
	}
	if d == nil { // out of rounds: the piece fails
		return DispatchInfo{Piece: p.Piece, Round: p.Reworks, Status: store.PieceFailed, Reason: p.Reason}, nil
	}
	if err := c.runs.launch(*d); err != nil {
		return nil, err
	}
	return DispatchInfo{DispatchID: d.ID, Piece: d.Piece, Round: d.Round, Status: store.DispatchRunning}, nil
}

// p0 is the piece's rework count so far, which names the round being rendered.
func p0(c *callCtx, piece string) int {
	p, err := c.st.GetPiece(c.run.ctx.Job, piece)
	if err != nil {
		return 0
	}
	return p.Reworks
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
	// The Warden is told how many flags were taken, not shown them back.
	return map[string]any{"handback_id": h.ID, "status": h.Status, "flags_recorded": len(flags), "parked": park != ""}, nil
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
	return struct {
		store.Handback
		Flags []store.Flag `json:"flags"`
	}{h, flags}, nil
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

// launch starts the Wright run for a dispatch on the configured Runner. The
// run ends when the Runner returns; a dispatch that never reported then fails.
func (r *Runs) launch(d store.Dispatch) error {
	r.mu.Lock()
	runner, root := r.runner, r.spoolRoot
	r.mu.Unlock()
	if runner == nil {
		return fmt.Errorf("%w: no runner is configured", errInvalidInput)
	}
	env, err := r.StartFor(RoleWright, root, RunContext{Job: d.JobID, Guild: d.Guild, DispatchID: d.ID})
	if err != nil {
		return err
	}
	if err := r.st.AttachRun(d.ID, env.Run); err != nil {
		r.End(env.Run)
		return err
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		runErr := runner.Run(r.bg, RunRequest{Env: env, Job: d.JobID, Piece: d.Piece, DispatchID: d.ID, Brief: d.Rendered})
		if cur, err := r.st.GetDispatch(d.ID); err == nil && cur.Status == store.DispatchRunning {
			msg := "the run ended without reporting a result"
			if runErr != nil {
				msg = "the run failed: " + runErr.Error()
			}
			raw, _ := json.Marshal(WrightResult{Status: ResultFailed, Uncertainty: msg})
			r.st.RecordResult(d.ID, raw)
		}
		r.End(env.Run)
	}()
	return nil
}
