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

// SessionRunner is the Runner that runs a Wright as an interactive Session of
// the vendor CLI its Guild is bound to, in a pane of the factory tmux session
// (ADR 0026). The Wright reports through the spool like any other; this
// launches it, follows its hook-derived state onto the run's timeline and the
// run's record, and returns once the Wright has finished its turn.
//
// The Session is left open when Run returns: it stays in its pane until the
// Job leaves active work (ADR 0029), which a later slice handles. Cancelling
// the run's context cancels the Session; returning does not. A piece sent
// back for rework is Nudged into its open Session, with the amended brief; if
// the Session is gone, a fresh one runs the amended brief (ADR 0029).
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

	mu   sync.Mutex
	live map[string]*liveSession // the open Session of each piece of each Job
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

func sessionKey(job, piece string) string { return job + "/" + piece }

// Reusable implements SessionReuser.
func (a *SessionRunner) Reusable(job, piece string) string {
	if ls := a.open(job, piece); ls != nil {
		return ls.worktree
	}
	return ""
}

// open is the piece's Session if it has not ended.
func (a *SessionRunner) open(job, piece string) *liveSession {
	a.mu.Lock()
	ls := a.live[sessionKey(job, piece)]
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
	g, err := a.Guilds(req.Guild)
	if err != nil {
		return err
	}
	r, ok := a.Runners[g.Binding.Runner]
	if !ok {
		if a.Fallback != nil {
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
	if ls := a.open(req.Job, req.Piece); ls != nil && req.Rework && ls.worktree == req.Worktree {
		c, err := a.nudge(ctx, req, ls)
		if !errors.Is(err, errSessionGone) {
			if err != nil {
				return err
			}
			return a.await(ctx, req, ls, c)
		}
		// The Session ended between the check and the Nudge: a fresh one runs the
		// amended brief, in the worktree and on the branch the round before left.
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
	ls := &liveSession{s: live, r: r, key: sessionKey(req.Job, req.Piece), worktree: req.Worktree}
	c := &carry{req: req, armed: true, done: make(chan error, 1)} // the brief was its first prompt
	ls.cur = c
	if req.Piece != "" && req.Env.Role == RoleWright {
		a.mu.Lock()
		if a.live == nil {
			a.live = map[string]*liveSession{}
		}
		a.live[sessionKey(req.Job, req.Piece)] = ls
		a.mu.Unlock()
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

// errSessionGone says the Session ended before the Nudge could be delivered.
var errSessionGone = errors.New("the Wright's Session has ended")

// nudge hands a rework to the piece's open Session: the amended brief, and the
// run it now reports through. It waits, up to NudgeWait, for a Session that is
// working or at a prompt to be idle, and types nothing at it meanwhile; one
// that is still not idle fails the run rather than being disturbed.
func (a *SessionRunner) nudge(ctx context.Context, req RunRequest, ls *liveSession) (*carry, error) {
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
	text := reworkNudge(req)
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
			return fail(fmt.Errorf("nudging the Wright's Session: %w", err))
		} else if time.Now().After(deadline) {
			return fail(fmt.Errorf("the Wright's Session is not idle (%s) after %s: it was left alone, and the rework was not delivered", st, wait))
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
	var w strings.Builder
	w.WriteString("Your last result for this piece was sent back for rework. The amended brief below replaces the one you were given; carry it out in this Session.\n\n")
	w.WriteString(strings.TrimSpace(req.Brief))
	w.WriteString("\n\n## Reporting this round\n\nThis rework is a new run, so the variables you started with no longer work. ")
	w.WriteString("Set these before any `archwright tool` call, report_result and ask included, and leave them set:\n\n")
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
	a.mu.Lock()
	if a.live[ls.key] == ls {
		delete(a.live, ls.key)
	}
	a.mu.Unlock()
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
