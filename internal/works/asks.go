package works

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// Who answered an ask, as recorded on it. Answers by the Archwright and by the
// dashboard reach the same Works path; the first one wins (ADR 0015).
const (
	AnsweredByDashboard = "dashboard"
)

// askTools are the asks of ADR 0015: any agent raises one, and the Works
// routes it Wright to Warden to Archwright to developer and the answer back.
var askTools = map[string]tool{
	"ask":            {roles: []string{RoleWright, RoleWarden}, call: raiseAsk},
	"list_asks":      {roles: []string{RoleWarden, RoleArchwright}, call: listAsks},
	"show_ask":       {roles: []string{RoleWarden, RoleArchwright}, call: showAsk},
	"answer_ask":     {roles: []string{RoleWarden, RoleArchwright}, call: answerAsk},
	"escalate_ask":   {roles: []string{RoleWarden}, call: escalateAsk},
	"accept_default": {roles: []string{RoleArchwright}, call: acceptDefault},
	"skip_ask":       {roles: []string{RoleArchwright}, call: skipAsk},
	"cancel_ask":     {roles: []string{RoleArchwright}, call: cancelAsk},
}

func init() {
	for name, t := range askTools {
		catalogue[name] = t
	}
}

// AskReceipt is the answer to ask: the ask returns at once, because waiting is parking.
type AskReceipt struct {
	AskID    string `json:"ask_id"`
	Holder   string `json:"holder"`
	Blocking bool   `json:"blocking"`
	Next     string `json:"next"`
}

func raiseAsk(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Kind      string           `json:"kind"`
		Blocking  *bool            `json:"blocking"`
		Why       string           `json:"why"`
		Questions []store.Question `json:"questions"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if err := requireField("kind", a.Kind); err != nil {
		return nil, err
	}
	if a.Blocking == nil {
		return nil, fmt.Errorf("%w: blocking is required (true parks your task until answered)", errInvalidInput)
	}
	if c.run.ctx.Job == "" {
		return nil, fmt.Errorf("%w: this run is not bound to a Job", errInvalidInput)
	}
	n := store.NewAsk{JobID: c.run.ctx.Job, Asker: c.run.role, Guild: c.run.ctx.Guild, Kind: a.Kind, Blocking: *a.Blocking, Why: a.Why, Questions: a.Questions}
	if c.run.role == RoleWright {
		if c.run.ctx.DispatchID == "" {
			return nil, fmt.Errorf("%w: this run carries out no dispatch", errInvalidInput)
		}
		d, err := c.st.GetDispatch(c.run.ctx.DispatchID)
		if err != nil {
			return nil, err
		}
		n.Piece, n.DispatchID = d.Piece, d.ID
	}
	ask, err := c.st.CreateAsk(n)
	if err != nil {
		return nil, err
	}
	next := "Carry on; the answer will be in a later brief."
	if ask.Blocking {
		next = "Stop now and end your run without reporting a result: your task is parked, and a fresh run starts with this brief plus the answers."
	}
	return AskReceipt{AskID: ask.ID, Holder: ask.Holder, Blocking: ask.Blocking, Next: next}, nil
}

// askFor loads an ask the calling run may see: a Warden's own Job and Guild, or any for the Archwright.
func (c *callCtx) askFor(id string) (store.Ask, error) {
	if err := requireField("ask_id", id); err != nil {
		return store.Ask{}, err
	}
	a, err := c.st.GetAsk(id)
	if err != nil {
		return store.Ask{}, err
	}
	if c.run.role == RoleWarden && (a.JobID != c.run.ctx.Job || a.Guild != c.run.ctx.Guild) {
		return store.Ask{}, fmt.Errorf("ask %q: %w", id, store.ErrNotFound)
	}
	return a, nil
}

func askIDArg(in json.RawMessage) (string, error) {
	var a struct {
		AskID string `json:"ask_id"`
	}
	return a.AskID, firstErr(decode(in, &a), func() error { return requireField("ask_id", a.AskID) })
}

func firstErr(err error, next func() error) error {
	if err != nil {
		return err
	}
	return next()
}

func listAsks(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
		Holder string `json:"holder"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	f := store.AskFilter{Job: a.JobID, Status: a.Status, Holder: a.Holder}
	if c.run.role == RoleWarden {
		f.Job, f.Guild = c.run.ctx.Job, c.run.ctx.Guild
	}
	return c.st.Asks(f)
}

func showAsk(c *callCtx, in json.RawMessage) (any, error) {
	id, err := askIDArg(in)
	if err != nil {
		return nil, err
	}
	return c.askFor(id)
}

func answerAsk(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		AskID   string         `json:"ask_id"`
		Answers []store.Answer `json:"answers"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if _, err := c.askFor(a.AskID); err != nil {
		return nil, err
	}
	return c.runs.AnswerAsk(a.AskID, c.run.role, c.run.role, a.Answers)
}

func escalateAsk(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		AskID string `json:"ask_id"`
		Why   string `json:"why"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if _, err := c.askFor(a.AskID); err != nil {
		return nil, err
	}
	child, err := c.st.EscalateAsk(a.AskID, a.Why)
	if err != nil {
		return nil, err
	}
	return AskReceipt{AskID: child.ID, Holder: child.Holder, Blocking: child.Blocking,
		Next: "The Archwright will put it to the developer; read the answer with show_ask on the original ask."}, nil
}

func acceptDefault(c *callCtx, in json.RawMessage) (any, error) {
	id, err := askIDArg(in)
	if err != nil {
		return nil, err
	}
	return c.runs.AcceptDefault(id, c.run.role)
}

func skipAsk(c *callCtx, in json.RawMessage) (any, error) {
	id, err := askIDArg(in)
	if err != nil {
		return nil, err
	}
	return c.runs.SkipAsk(id, c.run.role)
}

func cancelAsk(c *callCtx, in json.RawMessage) (any, error) {
	id, err := askIDArg(in)
	if err != nil {
		return nil, err
	}
	return c.runs.CancelAsk(id, c.run.role)
}

// level is the layer an answerer speaks for: the dashboard and the
// Archwright both answer for the developer.
func level(by string) string {
	if by == RoleWarden {
		return store.HolderWarden
	}
	return store.HolderArchwright
}

// AnswerAsk records the first answer to an ask and starts the fresh runs that
// replace blocked ones. A later answer to the same ask is a conflict.
func (r *Runs) AnswerAsk(id, by, who string, answers []store.Answer) (store.AnswerResult, error) {
	res, err := r.st.AnswerAsk(id, level(by), who, answers, renderResume)
	if err != nil {
		return store.AnswerResult{}, err
	}
	r.launchResumed(res.Resumed)
	return res, nil
}

// AcceptDefault answers every question of an ask with its recommended default.
func (r *Runs) AcceptDefault(id, who string) (store.AnswerResult, error) {
	a, err := r.st.GetAsk(id)
	if err != nil {
		return store.AnswerResult{}, err
	}
	if a.Status != store.AskOpen { // report who got there first, not a missing default
		return r.AnswerAsk(id, RoleArchwright, who, nil)
	}
	answers, err := store.DefaultAnswers(a.Questions)
	if err != nil {
		return store.AnswerResult{}, err
	}
	return r.AnswerAsk(id, RoleArchwright, who, answers)
}

// SkipAsk skips every question of an ask: the next brief says they are unanswered.
func (r *Runs) SkipAsk(id, who string) (store.AnswerResult, error) {
	a, err := r.st.GetAsk(id)
	if err != nil {
		return store.AnswerResult{}, err
	}
	return r.AnswerAsk(id, RoleArchwright, who, store.SkipAnswers(a.Questions))
}

// CancelAsk cancels an ask's Job: its open asks are cancelled and the Job is discarded.
func (r *Runs) CancelAsk(id, who string) (map[string]any, error) {
	a, err := r.st.GetAsk(id)
	if err != nil {
		return nil, err
	}
	if a.Status != store.AskCancelled { // a cancelled ask whose Job is still open is a retry of the close
		if _, err := r.st.CancelAsk(id, who); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	sm := r.sites
	r.mu.Unlock()
	job, err := r.st.GetJob(a.JobID)
	if err != nil {
		return nil, err
	}
	if job.Outcome == "" {
		if job.Site != "" && sm != nil {
			_, err = sm.Close(job.ID, "discarded")
		} else {
			err = r.st.CloseJob(job.ID, "discarded")
		}
		if err != nil {
			return nil, fmt.Errorf("ask cancelled but the Job could not be closed (close it again): %w", err)
		}
	}
	return map[string]any{"ask_id": id, "job_id": a.JobID, "status": store.AskCancelled, "job_outcome": "discarded"}, nil
}

func (r *Runs) launchResumed(ds []store.Dispatch) {
	for _, d := range ds {
		if _, err := r.launch(d); err != nil {
			log.Printf("dispatch %s: resuming after an answer: %v", d.ID, err)
		}
	}
}

const qaMarker = "\n## Questions and answers\n"

// renderResume is the brief of the fresh run that follows an answered ask.
func renderResume(prev store.Dispatch, qa []store.Ask) string {
	base := prev.Rendered
	if i := strings.Index(base, qaMarker); i >= 0 {
		base = base[:i]
	}
	return base + qaSection(qa)
}

// qaSection is the accumulated question-and-answer record of a piece, which
// every later brief for it carries.
func qaSection(asks []store.Ask) string {
	var w strings.Builder
	for _, a := range asks {
		if a.Status != store.AskAnswered {
			continue
		}
		if w.Len() == 0 {
			w.WriteString(qaMarker + "\nThese were asked on this piece earlier and are answered; do not ask them again. A skipped question stays unanswered: decide it yourself, or ask again only if it blocks you.\n")
		}
		fmt.Fprintf(&w, "\n### %s (%s)\n", a.ID, a.Kind)
		answers := map[string]store.Answer{}
		for _, an := range a.Answers {
			answers[an.QuestionID] = an
		}
		for _, q := range a.Questions {
			an := answers[q.ID]
			reply := strings.Join(an.Values, ", ")
			if an.Skipped || len(an.Values) == 0 {
				reply = "(skipped: unanswered)"
			}
			fmt.Fprintf(&w, "- Q: %s\n  A: %s\n", q.Text, reply)
		}
	}
	return w.String()
}
