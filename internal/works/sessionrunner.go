package works

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	hosted "github.com/phallatlassian/archwright/internal/session"
)

// SessionRunner is the Runner that runs a Wright, a Warden or a review as an
// interactive Session of the vendor CLI its Guild is bound to, in a pane of the
// factory tmux session (ADR 0026). The agent reports through the spool like
// any other; this launches it, follows its hook-derived state onto the run's
// timeline and the run's record, and returns once it has finished its turn.
//
// The Session is left open when a run returns: it stays in its pane until the
// Job leaves active work (ADR 0029), when CloseJob ends it. Cancelling the
// run's context cancels the Session; returning does not. A piece sent back for
// rework is Nudged into its Wright's open Session, with the amended brief; a
// Job's later steps for a Guild, and its later reviews, are Nudged into the
// Job's standing Warden Session for that Guild; if the Session is gone, a
// fresh one runs the brief (ADR 0029).
type SessionRunner struct {
	Runners map[string]hosted.Runner // by the runner name a Guild binding gives
	Guilds  GuildSource
	// Record keeps where a run's Session lives and what it is doing; nil ignores it.
	Record func(run string, info hosted.Info, state hosted.State)
	// Fallback carries out the runs of a runner that has no Session adapter;
	// nil fails them.
	Fallback Runner
	// NudgeWait is how long a rework waits for its Session to be idle before
	// giving up on it; zero means DefaultNudgeWait.
	NudgeWait time.Duration

	mu    sync.Mutex
	live  map[string]*liveSession  // the open Session of each piece of each Job, and each Guild's Warden of each Job
	locks map[string]chan struct{} // one run at a time for each Session key
}

// DefaultNudgeWait is how long a rework waits for a Wright Session that is
// working, or at a prompt, to be idle. A Session that is not idle by then is
// left alone and the rework fails: it is never typed at, and a second Session
// is never started in its worktree.
const DefaultNudgeWait = 2 * time.Minute

// nudgePoll is how often a rework looks again at a Session that was not idle.
const nudgePoll = 50 * time.Millisecond

// liveSession is a Wright's open Session, followed for as long as it lives.
// The run it is carrying out changes with each rework; its events go to
// whichever run holds it now.
type liveSession struct {
	job      string
	s        hosted.Session
	r        hosted.Runner
	key      string // where the Runner keeps it
	worktree string

	mu  sync.Mutex
	cur *carry
}

// carry is one run's hold on a Session: the run is done when the Session has
// finished a turn beyond the ones it had when the run took it over.
type carry struct {
	req      RunRequest
	base     int        // the Session's turns before this run's prompt
	armed    bool       // the run's prompt has been delivered
	finished bool       // the run has returned; later states are only recorded
	done     chan error // receives the run's outcome, once
}

// finish ends the run's hold with its outcome, once. The caller holds the Session's lock.
func (c *carry) finish(err error) {
	if !c.finished {
		c.finished = true
		c.done <- err
	}
}

// A Wright's Session is keyed by its piece and a Warden's by its Guild, each
// under the Job, so two Jobs never share one and a Review Warden (Hallmark)
// never lands in a builder's Session (Masonry's Warden or any Wright).
func wrightKey(job, piece string) string { return job + "/wright/" + piece }
func wardenKey(job, guild string) string { return job + "/warden/" + guild }

// runSpec says how a run takes its Session: which one it may re-use, and what
// it tells that Session.
type runSpec struct {
	key   string                      // where the Session is kept; "" for one that is not kept
	reuse func(ls *liveSession) bool  // whether the run is Nudged into an open Session under key
	nudge func(req RunRequest) string // what the open Session is told
}

// Reusable implements SessionReuser.
func (a *SessionRunner) Reusable(job, piece string) string {
	if ls := a.open(wrightKey(job, piece)); ls != nil {
		return ls.worktree
	}
	return ""
}

// open is the Session kept under key, if it has not ended.
func (a *SessionRunner) open(key string) *liveSession {
	a.mu.Lock()
	ls := a.live[key]
	a.mu.Unlock()
	if ls == nil || ls.s.State() == hosted.StateEnded {
		return nil
	}
	return ls
}

// Run implements Runner. The dispatch's outcome never depends on the agent's
// prose: it is whether the Wright reported through the spool, which the caller
// checks, and how the Session ended.
func (a *SessionRunner) Run(ctx context.Context, req RunRequest) error {
	spec := runSpec{reuse: func(ls *liveSession) bool { return req.Rework && ls.worktree == req.Worktree }, nudge: reworkNudge}
	if req.Piece != "" && req.Env.Role == RoleWright {
		spec.key = wrightKey(req.Job, req.Piece)
	}
	return a.run(ctx, req, spec)
}

// RunWarden implements WardenRunner: the step is carried out by the Job's
// standing Warden Session for its Guild, which is launched for the first step
// and Nudged with each later one (ADR 0029).
func (a *SessionRunner) RunWarden(ctx context.Context, req WardenRequest) error {
	return a.run(ctx, RunRequest{Env: req.Env, Job: req.Job, Guild: req.Guild, Piece: req.Step, Worktree: req.Worktree, Brief: req.Brief, ExtraEnv: req.ExtraEnv, Trace: req.Trace}, a.standing(req.Job, req.Guild, stepNudge))
}

// RunReview implements ReviewRunner: a read-only Session of the Review Guild,
// which shares no context with the builder because it is the Review Guild's
// own Warden Session, never a Wright's or a builder Guild's. Its second pass
// is Nudged into the same Session when it reviews the same checkout; a review
// in another checkout replaces it, so the Session's directory and skills are
// that review's.
func (a *SessionRunner) RunReview(ctx context.Context, req ReviewRequest) error {
	spec := a.standing(req.Job, ReviewGuild, reviewNudge)
	spec.reuse = func(ls *liveSession) bool { return ls.worktree == req.Worktree }
	return a.run(ctx, RunRequest{Env: req.Env, Job: req.Job, Guild: ReviewGuild, Worktree: req.Worktree, Brief: req.Brief, ExtraEnv: req.ExtraEnv, Trace: req.Trace}, spec)
}

func (a *SessionRunner) standing(job, guild string, nudge func(RunRequest) string) runSpec {
	return runSpec{key: wardenKey(job, guild), reuse: func(*liveSession) bool { return true }, nudge: nudge}
}

func (a *SessionRunner) run(ctx context.Context, req RunRequest, spec runSpec) error {
	g, err := a.Guilds(req.Guild)
	if err != nil {
		return err
	}
	r, ok := a.Runners[g.Binding.Runner]
	if !ok {
		if a.Fallback != nil && req.Env.Role == RoleWright {
			return a.Fallback.Run(ctx, req)
		}
		return fmt.Errorf("guild %q is bound to runner %q, which cannot run as a Session", g.Name, g.Binding.Runner)
	}
	if req.Worktree == "" {
		return fmt.Errorf("the run has no worktree to work in")
	}
	spawn, err := spawnFor(g.Binding.Runner, g)
	if err != nil {
		return err
	}
	if spec.key != "" {
		// A Session carries out one run at a time: a Job's steps for a Guild are
		// serialised in its Warden (ADR 0029), and two runs never both find no
		// Session and launch two.
		unlock, err := a.lockKey(ctx, spec.key)
		if err != nil {
			return err
		}
		defer unlock()
		if ls := a.open(spec.key); ls != nil && spec.reuse(ls) {
			c, err := a.nudge(ctx, req, ls, spec.nudge(req))
			if !errors.Is(err, errSessionGone) {
				if err != nil {
					return err
				}
				return a.await(ctx, req, ls, c)
			}
			// The Session ended between the check and the Nudge: a fresh one runs the
			// amended brief, in the worktree and on the branch the round before left.
		}
	}
	env := map[string]string{}
	for _, kv := range req.Env.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for k, v := range req.ExtraEnv {
		env[k] = v
	}
	live, err := r.Launch(ctx, hosted.LaunchRequest{
		Run: req.Env.Run, Job: req.Job, Role: req.Env.Role, Brief: req.Brief, Dir: req.Worktree, Env: env, Spawn: spawn,
	})
	if err != nil {
		return fmt.Errorf("%s was not started: %w", r.Name(), err) // a never-list refusal fails the dispatch closed
	}
	a.record(req, live.Info(), hosted.StateStarting)
	ls := &liveSession{job: req.Job, s: live, r: r, key: spec.key, worktree: req.Worktree}
	c := &carry{req: req, armed: true, done: make(chan error, 1)} // the brief was its first prompt
	ls.cur = c
	if spec.key != "" {
		a.mu.Lock()
		if a.live == nil {
			a.live = map[string]*liveSession{}
		}
		old := a.live[spec.key]
		a.live[spec.key] = ls
		a.mu.Unlock()
		if old != nil {
			old.s.Cancel() // an ended Session's dead pane goes; it has nothing left to say
		}
	}
	go a.follow(ls)
	return a.await(ctx, req, ls, c)
}

// await returns when the run's turn is finished, or when the context cancels
// the Session.
func (a *SessionRunner) await(ctx context.Context, req RunRequest, ls *liveSession, c *carry) error {
	select {
	case err := <-c.done:
		return err
	case <-ctx.Done():
		ls.s.Cancel()
		ls.s.Result()
		a.record(req, ls.s.Info(), hosted.StateEnded)
		return context.Canceled
	}
}

// lockKey waits for the run holding the Session key, or for ctx.
func (a *SessionRunner) lockKey(ctx context.Context, key string) (unlock func(), err error) {
	a.mu.Lock()
	if a.locks == nil {
		a.locks = map[string]chan struct{}{}
	}
	l := a.locks[key]
	if l == nil {
		l = make(chan struct{}, 1)
		a.locks[key] = l
	}
	a.mu.Unlock()
	select {
	case l <- struct{}{}:
		return func() { <-l }, nil
	case <-ctx.Done():
		return nil, context.Canceled
	}
}

// OpenJobs lists the Jobs that hold a Session, open or ended with its pane still kept.
func (a *SessionRunner) OpenJobs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := map[string]bool{}
	var jobs []string
	for _, ls := range a.live {
		if !seen[ls.job] {
			seen[ls.job] = true
			jobs = append(jobs, ls.job)
		}
	}
	return jobs
}

// CloseJob ends the Sessions of a Job that has left active work (ADR 0029):
// each one that is idle, or already ended, exits and its pane is removed, which
// closes the Job's window with the last of them. A Session that is working, at
// a prompt or still starting is left alone, and so is its entry; the count of
// those is returned, for the caller to ask again.
func (a *SessionRunner) CloseJob(job string) (remaining int) {
	a.mu.Lock()
	var mine []*liveSession
	for _, ls := range a.live {
		if ls.job == job {
			mine = append(mine, ls)
		}
	}
	a.mu.Unlock()
	for _, ls := range mine {
		if st := ls.s.State(); st != hosted.StateIdle && st != hosted.StateEnded {
			remaining++
			continue
		}
		ls.s.Cancel()
		ls.s.Result() // the pane is gone once the result is
		a.mu.Lock()
		if a.live[ls.key] == ls {
			delete(a.live, ls.key)
		}
		a.mu.Unlock()
	}
	return remaining
}

// errSessionGone says the Session ended before the Nudge could be delivered.
var errSessionGone = errors.New("the Session has ended")

// nudge hands a run to its open Session: text names the brief, and the run it
// now reports through. It waits, up to NudgeWait, for a Session that is
// working or at a prompt to be idle, and types nothing at it meanwhile; one
// that is still not idle fails the run rather than being disturbed.
func (a *SessionRunner) nudge(ctx context.Context, req RunRequest, ls *liveSession, text string) (*carry, error) {
	c := &carry{req: req, done: make(chan error, 1)}
	ls.mu.Lock()
	prev := ls.cur
	ls.mu.Unlock()
	fail := func(err error) (*carry, error) { // the run did not take the Session: its events stay with the run that had it
		ls.mu.Lock()
		if ls.cur == c {
			ls.cur = prev
		}
		ls.mu.Unlock()
		return nil, err
	}
	wait := a.NudgeWait
	if wait <= 0 {
		wait = DefaultNudgeWait
	}
	deadline := time.Now().Add(wait)
	for {
		ls.mu.Lock()
		c.base = ls.s.Info().Turns // while idle, the turns before this prompt
		ls.cur = c
		ls.mu.Unlock()
		err := ls.s.Nudge(text)
		if err == nil {
			ls.arm(c)
			return c, nil
		}
		if st := ls.s.State(); st == hosted.StateEnded {
			return fail(errSessionGone)
		} else if !errors.Is(err, hosted.ErrNotIdle) {
			return fail(fmt.Errorf("nudging the Session: %w", err))
		} else if time.Now().After(deadline) {
			return fail(fmt.Errorf("the Session is not idle (%s) after %s: it was left alone, and the run was not delivered to it", st, wait))
		}
		select {
		case <-ctx.Done():
			return fail(context.Canceled)
		case <-time.After(nudgePoll):
		}
	}
}

// reworkNudge is what a Wright Session is told when its piece is sent back: the
// amended brief, and the run its tool calls belong to from now on. The Session
// started with the variables of its first run, which have ended.
func reworkNudge(req RunRequest) string {
	return nudgeText("Your last result for this piece was sent back for rework. The amended brief below replaces the one you were given; carry it out in this Session.", "this round", req)
}

// stepNudge is what a Job's standing Warden is told for its next step.
func stepNudge(req RunRequest) string {
	return nudgeText("You are the standing Warden of this Job for your Guild, and the Works has a new step for you. Everything you learned in earlier steps still holds; the brief below is this step's. Carry it out in this Session.", "this step", req)
}

// reviewNudge is what the Review Warden is told for a further review of the Job.
func reviewNudge(req RunRequest) string {
	return nudgeText("The Works has a further review for you, of the Job's code as it now stands. You have reviewed this Job before and may use that; judge this round on the code named below. The brief below is this review's.", "this review", req)
}

func nudgeText(lead, what string, req RunRequest) string {
	var w strings.Builder
	w.WriteString(lead + "\n\n")
	w.WriteString(strings.TrimSpace(req.Brief))
	w.WriteString("\n\n## Reporting " + what + "\n\nThis is a new run, so the variables you started with no longer work. ")
	w.WriteString("Set these before any `archwright tool` call, handback, report_review and ask included, and leave them set:\n\n")
	for _, kv := range req.Env.Environ() {
		w.WriteString("export " + kv + "\n")
	}
	return w.String()
}

// arm marks the run's prompt as delivered; if the Session has already finished
// the turn it started, the run is done now.
func (ls *liveSession) arm(c *carry) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	c.armed = true
	ls.settleLocked(c, ls.s.State())
}

// settleLocked finishes c when its turn is over: the Session is idle and has
// finished a turn since c took it over.
func (ls *liveSession) settleLocked(c *carry, state hosted.State) {
	if c.armed && state == hosted.StateIdle && ls.s.Info().Turns > c.base {
		c.finish(nil)
	}
}

// follow carries a Session's events to the run that holds it, until it ends.
func (a *SessionRunner) follow(ls *liveSession) {
	for e := range ls.s.Events() {
		ls.mu.Lock()
		c := ls.cur
		emit := c != nil && !c.finished // a finished run only has its state kept up to date
		ls.mu.Unlock()
		if c == nil {
			continue
		}
		if emit {
			c.req.Trace.Emit(timelineKind(e), summarizeSession(e), rawOfSession(e))
		}
		if e.Kind == hosted.KindState {
			a.record(c.req, ls.s.Info(), e.State) // the state this entry reports, so the record never runs ahead of the timeline
			ls.mu.Lock()
			if ls.cur == c {
				ls.settleLocked(c, e.State)
			}
			ls.mu.Unlock()
		}
	}
	ls.mu.Lock()
	c := ls.cur
	ls.mu.Unlock()
	if c == nil {
		return
	}
	a.record(c.req, ls.s.Info(), hosted.StateEnded)
	ls.mu.Lock()
	c.finish(a.ended(ls.r, ls.s))
	ls.mu.Unlock()
}

func (a *SessionRunner) record(req RunRequest, info hosted.Info, state hosted.State) {
	if a.Record != nil {
		a.Record(req.Env.Run, info, state)
	}
}

// ended is how a Session that ended on its own turns into the dispatch's error.
func (a *SessionRunner) ended(r hosted.Runner, s hosted.Session) error {
	switch res := s.Result(); res.Status {
	case hosted.StatusCancelled:
		return context.Canceled
	case hosted.StatusFailed:
		return fmt.Errorf("%s Session failed: %s", r.Name(), res.Error)
	}
	return nil
}

// timelineKind is the run timeline's kind for a Session event.
func timelineKind(e hosted.Event) string {
	if e.Kind == hosted.KindState {
		return "session"
	}
	return string(e.Kind)
}

func summarizeSession(e hosted.Event) string {
	switch e.Kind {
	case hosted.KindPosture:
		if p := e.Posture; p != nil {
			return fmt.Sprintf("%s %s: %s mode, %s sandbox", p.Runner, p.Version, p.Mode, p.Sandbox)
		}
	case hosted.KindState:
		s := string(e.State)
		if e.State == hosted.StateWaitingOnPermission {
			s = "waiting on permission: " + strings.TrimSpace(e.Tool+": "+e.Command)
		}
		return clipLine(s)
	case hosted.KindNote:
		return clipLine(e.Text)
	}
	return string(e.Kind)
}

// clipLine is the first line of s, cut to what a timeline entry shows.
func clipLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > summaryMax {
		s = string(r[:summaryMax]) + "…"
	}
	return s
}

// rawOfSession is what is kept behind a timeline entry: the hook payload
// where there is one, otherwise the event.
func rawOfSession(e hosted.Event) json.RawMessage {
	if len(e.Raw) > 0 {
		return e.Raw
	}
	b, _ := json.Marshal(e)
	return b
}
