package works

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/runner"
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
//
// A Works that stops leaves the Sessions open in their panes (Detach), and the
// next one takes them over (Restore): a live pane is adopted, one whose pane is
// gone is resumed by its session id when its piece or step is next run, and
// failing that a fresh Session runs the stored brief.
type SessionRunner struct {
	Runners map[string]hosted.Runner // by the runner name a Guild binding gives
	Guilds  GuildSource
	// Record keeps where a run's Session lives and what it is doing; nil ignores it.
	Record func(run string, info hosted.Info, state hosted.State)
	// NudgeWait is how long a rework waits for its Session to be idle before
	// giving up on it; zero means DefaultNudgeWait.
	NudgeWait time.Duration

	mu        sync.Mutex
	live      map[string]*liveSession  // the open Session of each piece of each Job, and each Guild's Warden of each Job
	locks     map[string]chan struct{} // one run at a time for each Session key
	resumable map[string]*resumable    // Sessions of an earlier Works whose pane is gone, by the key of the Session they were
	detached  atomic.Bool              // the Works has stopped: Sessions are left alone and nothing more is recorded
}

// UseBaseEnv gives every Runner that takes one the developer's shell
// environment as the Works keeps it (envsnap), so a Session is spawned with
// the allow-listed snapshot and never the Works' own environment.
func (a *SessionRunner) UseBaseEnv(f func() map[string]string) {
	for _, rn := range a.Runners {
		if u, ok := rn.(hosted.BaseEnvUser); ok {
			u.UseBaseEnv(f)
		}
	}
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

	tailMu sync.Mutex
	tail   transcriptTail // how far the Session's transcript has been read onto the timeline
	said   string         // the full text of the last message the transcript gave the timeline, if its raw held it all

	mu    sync.Mutex
	cur   *carry
	turns int // turns the Session had finished at the latest event followed
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

// Reusable implements SessionReuser: the worktree of the piece's open Session,
// or of the Session of an earlier Works that can be resumed there.
func (a *SessionRunner) Reusable(job, piece string) string {
	key := wrightKey(job, piece)
	if ls := a.open(key); ls != nil {
		return ls.worktree
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if rs := a.resumable[key]; rs != nil {
		return rs.dir
	}
	return ""
}

// Detach makes the Runner leave every Session alone: it is called as the Works
// stops, with the Sessions still open in their panes for the next Works.
func (a *SessionRunner) Detach() { a.detached.Store(true) }

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
	switch {
	case req.Restarted:
		spec.nudge = restartNudge
	case req.Answered:
		spec.nudge = answerNudge
	}
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
		return fmt.Errorf("guild %q is bound to runner %q, which cannot run as a Session", g.Name, g.Binding.Runner)
	}
	if req.Worktree == "" {
		return fmt.Errorf("the run has no worktree to work in")
	}
	spawn, err := spawnFor(g.Binding.Runner, g)
	if err != nil {
		return err
	}
	var rs *resumable
	if spec.key != "" {
		// A Session carries out one run at a time: a Job's steps for a Guild are
		// serialised in its Warden (ADR 0029), and two runs never both find no
		// Session and launch two.
		unlock, err := a.lockKey(ctx, spec.key)
		if err != nil {
			a.cancelOpen(req, a.open(spec.key), err)
			return err
		}
		defer unlock()
		if ls := a.open(spec.key); ls != nil && spec.reuse(ls) {
			c, err := a.nudge(ctx, req, ls, spec.nudge(req))
			if !errors.Is(err, errSessionGone) {
				if err != nil {
					a.cancelOpen(req, ls, err)
					return err
				}
				return a.await(ctx, req, ls, c)
			}
			// The Session ended between the check and the Nudge: a fresh one runs the
			// amended brief, in the worktree and on the branch the round before left.
		}
		rs = a.takeResumable(spec.key, r, spec)
	}
	env := map[string]string{}
	for _, kv := range req.Env.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for k, v := range req.ExtraEnv {
		env[k] = v
	}
	lreq := hosted.LaunchRequest{Run: req.Env.Run, Job: req.Job, Role: req.Env.Role, Guild: req.Guild, Brief: req.Brief, Dir: req.Worktree, Env: env, Spawn: spawn}
	var live hosted.Session
	if rs != nil {
		// The Session of an earlier Works lost its pane: resume it by id, in the
		// directory it worked in, told what the Nudge of a live one would be. If the
		// vendor cannot resume it, a fresh Session runs the stored brief instead.
		rreq := lreq
		rreq.Resume, rreq.Dir, rreq.Brief = rs.id, rs.dir, spec.nudge(req)
		s, err := r.Launch(ctx, rreq)
		switch {
		case errors.Is(err, runner.ErrForbidden):
			return fmt.Errorf("%s was not started: %w", r.Name(), err)
		case err == nil && awaitStarted(ctx, s):
			live = s
			req.Trace.Emit("note", "resumed the Session "+rs.id+" its pane was lost from", nil)
		case ctx.Err() != nil:
			if s != nil {
				s.Cancel()
			}
			return stopped(ctx)
		default:
			if s != nil {
				s.Cancel() // clears the dead pane it left
			}
			req.Trace.Emit("note", "the Session "+rs.id+" could not be resumed; a fresh Session runs the brief", nil)
		}
	}
	if live == nil {
		var err error
		if live, err = r.Launch(ctx, lreq); err != nil {
			return fmt.Errorf("%s was not started: %w", r.Name(), err) // a never-list refusal fails the dispatch closed
		}
	}
	a.record(req, live.Info(), hosted.StateStarting)
	dir := live.Info().Dir
	if dir == "" {
		dir = req.Worktree
	}
	ls := &liveSession{job: req.Job, s: live, r: r, key: spec.key, worktree: dir}
	ls.tail.skip = rs != nil                                      // a resumed Session may go on writing the file it had
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

// stopped is why a run's context ended: the Works is stopping, which leaves
// the Session open, or the run was cancelled.
func stopped(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), ErrWorksStopping) {
		return fmt.Errorf("%w: its Session was left open in its pane", ErrWorksStopping)
	}
	return context.Canceled
}

// awaitStarted waits for a launched Session to report its start, and says
// whether it did: a Session that ends first never started.
func awaitStarted(ctx context.Context, s hosted.Session) bool {
	for {
		switch s.State() {
		case hosted.StateEnded:
			return false
		case hosted.StateStarting:
		default:
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(nudgePoll):
		}
	}
}

// await returns when the run's turn is finished, or when the context cancels
// the Session: a developer's cancel interrupts it and kills its pane after a
// grace (ADR 0029), while a stopping Works leaves it open for the next one.
func (a *SessionRunner) await(ctx context.Context, req RunRequest, ls *liveSession, c *carry) error {
	select {
	case err := <-c.done:
		return err
	case <-ctx.Done():
		if err := stopped(ctx); errors.Is(err, ErrWorksStopping) {
			return err
		}
		ls.s.Cancel()
		ls.s.Result()
		a.record(req, ls.s.Info(), hosted.StateEnded)
		return context.Canceled
	}
}

// cancelOpen ends the Session of a run that was cancelled before it took hold
// of it, while it waited for the Session to be idle or free: the Session is
// still working, or at a prompt, and the developer's cancel interrupts it and
// kills its pane (ADR 0029). A Works that is stopping leaves it open.
func (a *SessionRunner) cancelOpen(req RunRequest, ls *liveSession, err error) {
	if ls == nil || !errors.Is(err, context.Canceled) || errors.Is(err, ErrWorksStopping) {
		return
	}
	ls.s.Cancel()
	ls.s.Result()
	a.record(req, ls.s.Info(), hosted.StateEnded)
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
		return nil, stopped(ctx)
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
	ls.skipTranscript()
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
			return fail(stopped(ctx))
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

// answerNudge is what a Wright Session is told when the Ask it parked on is
// answered: the answers, and the run its tool calls belong to from now on. The
// Session already holds the brief, so only the Q&A record is sent.
func answerNudge(req RunRequest) string {
	req.Brief = qaRecord(req.Brief)
	return nudgeText("The question you asked was answered, and you are no longer parked. Carry on with the brief you were given, taking the answers below into account; do not ask them again.", "this round", req)
}

// restartNudge is what a Wright Session is told when the Works restarted under
// it: the brief it was running, and the run its tool calls belong to from now
// on, since the run it started under ended with the Works.
func restartNudge(req RunRequest) string {
	return nudgeText("The Works restarted while you were working on this piece, and the run you reported to has ended. Your Session is still here: carry on in it. The brief below is the one you were given, with where your work stands.", "from now on", req)
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
	if c.armed && state == hosted.StateIdle && ls.turns > c.base {
		c.finish(nil)
	}
}

// follow carries a Session's events to the run that holds it, until it ends.
func (a *SessionRunner) follow(ls *liveSession) {
	events := ls.s.Events()
	// Tool hooks that move no state make no event, so the transcript is also
	// read on a tick for what the agent does between them.
	tick := time.NewTicker(transcriptPoll)
	defer tick.Stop()
	for events != nil {
		var e hosted.Event
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			e = ev
		case <-tick.C:
			if !a.detached.Load() {
				ls.mu.Lock()
				c := ls.cur
				ls.mu.Unlock()
				if c != nil && !c.finished {
					ls.ingest(c)
				}
			}
			continue
		}
		if a.detached.Load() {
			continue // the Works has stopped: the Session is the next one's
		}
		ls.mu.Lock()
		c := ls.cur
		emit := c != nil && !c.finished // a finished run only has its state kept up to date
		ls.mu.Unlock()
		if c == nil {
			continue
		}
		if emit {
			ls.ingest(c) // what the agent did before this event comes first
			if e.Kind == hosted.KindState && e.Hook == hosted.HookStop && strings.TrimSpace(e.Text) != "" {
				// The transcript can lag the turn: look again for the last message before the run settles.
				for i := 0; i < stopRereads && !ls.saidAll(e.Text); i++ {
					time.Sleep(stopRereadWait)
					ls.ingest(c)
				}
			}
			c.req.Trace.Emit(timelineKind(e), summarizeSession(e), rawOfSession(e))
			if e.Kind == hosted.KindState && e.Hook == hosted.HookStop && strings.TrimSpace(e.Text) != "" && !ls.saidAll(e.Text) {
				// What the agent said ending its turn, kept as the run's last message: a
				// Session that ends without reporting still has its last words.
				raw, _ := json.Marshal(map[string]string{"result": e.Text})
				c.req.Trace.Emit("message", clipLine(e.Text), raw)
			}
		}
		if e.Kind == hosted.KindState {
			a.record(c.req, ls.s.Info(), e.State) // the state this entry reports, so the record never runs ahead of the timeline
			ls.mu.Lock()
			ls.turns = e.Turns // as the events report it, not read off the Session, which can be turns ahead of the follower
			if ls.cur == c {
				ls.settleLocked(c, e.State)
			}
			ls.mu.Unlock()
		}
	}
	if a.detached.Load() {
		return
	}
	ls.mu.Lock()
	c := ls.cur
	ls.mu.Unlock()
	if c == nil {
		return
	}
	if !c.finished {
		ls.ingest(c) // the tail the Session wrote before it ended
	}
	a.record(c.req, ls.s.Info(), hosted.StateEnded)
	ls.mu.Lock()
	c.finish(a.ended(ls.r, ls.s))
	ls.mu.Unlock()
}

// transcriptPoll is how often a Session's transcript is read between its events.
const transcriptPoll = 500 * time.Millisecond

// stopRereads and stopRereadWait bound how long a Stop waits for the transcript
// to show the message the hook carried.
const (
	stopRereads    = 5
	stopRereadWait = 100 * time.Millisecond
)

// ingest puts what the Session's agent has written to its transcript since the
// last read onto c's timeline. Only a Claude Session's transcript is read.
func (ls *liveSession) ingest(c *carry) {
	if ls.r == nil || ls.r.Name() != "claude" {
		return
	}
	ls.tailMu.Lock()
	ls.tail.follow(ls.s.Info().TranscriptPath)
	entries := ls.tail.read(false)
	for _, e := range entries {
		if e.kind == "message" {
			ls.said = e.whole
		}
	}
	ls.tailMu.Unlock()
	for _, e := range entries {
		c.req.Trace.Emit(e.kind, e.summary, e.raw)
	}
}

// saidAll is whether the last message the transcript gave the timeline carried
// all of text, which a Stop hook with the same words need not repeat.
func (ls *liveSession) saidAll(text string) bool {
	ls.tailMu.Lock()
	defer ls.tailMu.Unlock()
	return ls.said != "" && strings.TrimSpace(ls.said) == strings.TrimSpace(text)
}

// skipTranscript starts the next run at the end of the transcript, so it takes
// none of the lines of the run before it or of a person typing between.
func (ls *liveSession) skipTranscript() {
	if ls.r == nil || ls.r.Name() != "claude" {
		return
	}
	ls.tailMu.Lock()
	defer ls.tailMu.Unlock()
	ls.tail.follow(ls.s.Info().TranscriptPath)
	ls.tail.toEnd()
	ls.said = ""
}

func (a *SessionRunner) record(req RunRequest, info hosted.Info, state hosted.State) {
	if a.Record != nil && !a.detached.Load() {
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

// spawnFor is the permission posture a Guild's binding asks for. It can only
// narrow what the runner would do; the adapter refuses anything the never-list
// forbids, and a restriction the runner cannot enforce fails the run.
func spawnFor(runnerName string, g Guild) (runner.Spawn, error) {
	var s runner.Spawn
	if g.Binding.Access == defs.ReadOnly {
		s.Mode = "plan"
	}
	if len(g.Binding.DeniedTools) > 0 {
		if runnerName != "claude" {
			return s, fmt.Errorf("guild %q denies tools, which %s cannot enforce", g.Name, runnerName)
		}
		s.ExtraArgs = append(s.ExtraArgs, "--disallowed-tools", strings.Join(g.Binding.DeniedTools, ","))
	}
	return s, nil
}

// summaryMax is the longest a timeline entry's summary is.
const summaryMax = 200

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
	case hosted.KindNote, hosted.KindHuman:
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
