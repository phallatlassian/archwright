package session

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phallatlassian/archwright/internal/runner"
)

// Fake is a Runner that plays a script of hooks and agent actions instead of
// hosting a CLI. Its state comes from the same Machine as the tmux adapter's,
// so a Works test sees the transitions a real Session would make.
type Fake struct {
	vendor   string
	mu       sync.Mutex
	script   Script
	launches []LaunchRequest
	sessions []Session
	nudges   []string
	orphans  []Orphan
	known    map[string]bool
	// NudgeTimeout is how long a Nudge waits for the agent to report a prompt; default 5 s.
	NudgeTimeout time.Duration
}

// Script is what a Fake Session plays after launch.
type Script struct {
	Steps []Step
	// End plays SessionEnd after the steps and ends the Session, as an agent
	// that exits does. Otherwise the Session stays open until cancelled, as an
	// interactive one does after its turn.
	End bool
	// NoStart never plays SessionStart, as a Session stuck at a trust dialog.
	NoStart bool
	// NoResume fails a launch that asks to resume a session, as the vendor
	// does when it no longer has it.
	NoResume bool
	// Nudged is what the agent does each time it takes a Nudge: normally a
	// UserPromptSubmit, its work and a Stop. A Session with none takes the text
	// and never reports a prompt, as one that ignores it. An Act sees the
	// launch request with the ARCHWRIGHT_* variables the Nudge's text sets in
	// place of the ones the Session started with.
	Nudged []Step
}

// Step is one thing a scripted Session does: deliver a hook, act as the
// agent (tool calls through the spool), or wait.
type Step struct {
	Hook  *Hook
	Act   func(ctx context.Context, req LaunchRequest) error
	Pause time.Duration
	// AwaitQueue holds the agent busy until a message is queued for it.
	AwaitQueue bool
}

// Turn is the script of an agent that starts, takes the brief, does act and
// finishes its turn.
func Turn(act func(ctx context.Context, req LaunchRequest) error) Script {
	return Script{Steps: []Step{
		{Hook: &Hook{Event: HookSessionStart, SessionID: "fake-session", TranscriptPath: "/fake/transcript.jsonl"}},
		{Hook: &Hook{Event: HookUserPromptSubmit}},
		{Act: act},
		{Hook: &Hook{Event: HookStop}},
	}}
}

// NewFake plays script on every Session until SetScript replaces it. It
// stands in for Claude.
func NewFake(s Script) *Fake { return NewFakeFor("claude", s) }

// NewFakeFor is a Fake standing in for the named vendor. Only Codex's Sessions
// can be queued a message.
func NewFakeFor(vendor string, s Script) *Fake { return &Fake{vendor: vendor, script: s} }

// SetScript replaces what later Sessions play.
func (f *Fake) SetScript(s Script) {
	f.mu.Lock()
	f.script = s
	f.mu.Unlock()
}

// Launches returns every request launched so far.
func (f *Fake) Launches() []LaunchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LaunchRequest(nil), f.launches...)
}

// Sessions returns every Session launched so far.
func (f *Fake) Sessions() []Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Session(nil), f.sessions...)
}

// Nudges returns the text of every Nudge delivered so far, across Sessions.
func (f *Fake) Nudges() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.nudges...)
}

func (f *Fake) nudged(text string) {
	f.mu.Lock()
	f.nudges = append(f.nudges, text)
	f.mu.Unlock()
}

// Adopt implements Adopter: a Session of this Fake that has not ended is still
// in its pane, whichever Works launched it.
func (f *Fake) Adopt(pane string, _ ...string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.Info().Pane == pane && !isEnded(s) {
			if fs, ok := s.(*fakeSession); ok && f.vendor == "codex" {
				return queueingSession{fs}, nil
			}
			return s, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNoPane, pane)
}

func isEnded(s Session) bool {
	if c, ok := s.(interface{ isFinished() bool }); ok {
		return c.isFinished()
	}
	return s.State() == StateEnded
}

// AddOrphan makes a pane appear that no run accounts for.
func (f *Fake) AddOrphan(o Orphan) {
	f.mu.Lock()
	f.orphans = append(f.orphans, o)
	f.mu.Unlock()
}

// Known is the panes the last FlagOrphans was told the Works accounts for.
func (f *Fake) Known() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.known
}

// FlagOrphans implements Orphaner.
func (f *Fake) FlagOrphans(known map[string]bool) ([]Orphan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.known = known
	var out []Orphan
	for _, o := range f.orphans {
		if !known[o.Pane] {
			out = append(out, o)
		}
	}
	return out, nil
}

// Name implements Runner.
func (f *Fake) Name() string { return f.vendor }

// Capabilities implements Runner.
func (f *Fake) Capabilities() Capabilities {
	return Capabilities{Hooks: true, PermissionRequests: true, Resume: true, Queue: f.vendor == "codex"}
}

// Launch implements Runner.
func (f *Fake) Launch(ctx context.Context, req LaunchRequest) (Session, error) {
	if err := checkLaunch(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.launches = append(f.launches, req)
	script := f.script
	n := len(f.launches)
	f.mu.Unlock()
	acts, cancel := context.WithCancel(context.Background()) // the Session outlives the launch call
	timeout := f.NudgeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	s := &fakeSession{core: newCore(""), cancelAct: cancel, cancelCh: make(chan struct{}), queueCh: make(chan string, 8), nudgeCh: make(chan string, 1), nudgeTimeout: timeout, onNudge: f.nudged}
	s.core.setPane("%fake"+strconv.Itoa(n), "@fake")
	s.core.setPlace(req.Dir, f.vendor)
	s.core.add(Event{Kind: KindPosture, Posture: &runner.Posture{
		Runner: f.vendor, Version: "fake", Mode: req.Spawn.EffectiveMode(), Sandbox: runner.SandboxInherited,
		Flags: append([]string{}, req.Spawn.ExtraArgs...), Notices: []string{},
	}})
	f.mu.Lock()
	f.sessions = append(f.sessions, s)
	f.mu.Unlock()
	go s.play(acts, req, script)
	if f.vendor == "codex" {
		return queueingSession{s}, nil
	}
	return s, nil
}

type fakeSession struct {
	*core
	cancelAct context.CancelFunc
	cancelCh  chan struct{}
	once      sync.Once
	queueCh   chan string

	nudgeCh      chan string
	nudgeTimeout time.Duration
	onNudge      func(text string)
}

// queueingSession is a fakeSession of a vendor that has a queue.
type queueingSession struct{ *fakeSession }

// Queue implements Queuer: a working Session takes the message, any other
// refuses it.
func (s queueingSession) Queue(text string) error {
	if st := s.State(); st != StateWorking {
		return fmt.Errorf("%w: the Session is %s", ErrNotQueueable, st)
	}
	s.expectPrompt() // the turn this message becomes is the Works', not a human's
	select {
	case s.queueCh <- text:
		return nil
	default:
		s.retractPrompt()
		return fmt.Errorf("%w: the queue is full", ErrNotQueueable)
	}
}

// Nudge implements Session: an idle Session takes the text and plays its
// script's Nudged steps; any other refuses it.
func (s *fakeSession) Nudge(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("nothing to nudge")
	}
	if err := s.nudgeable(); err != nil {
		return err
	}
	before := s.prompts()
	s.expectPrompt()
	select {
	case s.nudgeCh <- text:
	default:
		s.retractPrompt()
		return fmt.Errorf("%w: a Nudge is already being taken", ErrNotIdle)
	}
	s.onNudge(text)
	return s.awaitPrompt(before, s.nudgeTimeout)
}

// Human plays a person typing a prompt into the pane: the Session takes the
// text as it does a Nudge, but the Works did not send it.
func (s *fakeSession) Human(text string) {
	select {
	case s.nudgeCh <- text:
	default:
	}
}

// Cancel implements Session.
func (s *fakeSession) Cancel() {
	s.once.Do(func() {
		s.cancelAct()
		close(s.cancelCh)
	})
}

func (s *fakeSession) play(ctx context.Context, req LaunchRequest, sc Script) {
	if sc.NoStart || (req.Resume != "" && sc.NoResume) {
		s.finish(Result{Status: runner.StatusFailed, ExitStatus: -1, Error: "failed to start: no SessionStart hook"})
		return
	}
	if !s.playSteps(ctx, req, sc.Steps) {
		return
	}
	if sc.End {
		s.feed(Hook{Event: HookSessionEnd})
		s.finish(Result{Status: runner.StatusDone, ExitStatus: 0})
		return
	}
	for {
		select {
		case text := <-s.nudgeCh:
			if !s.playSteps(ctx, nudgedRequest(req, text), sc.Nudged) {
				return
			}
		case <-s.cancelCh:
			s.finish(Result{Status: runner.StatusCancelled, ExitStatus: -1})
			return
		}
	}
}

// nudgeEnvLine is a line of a Nudge's text that sets a variable of the run.
var nudgeEnvLine = regexp.MustCompile(`(?m)^(?:export )?(ARCHWRIGHT_[A-Z_]+)=(\S+)$`)

// nudgedRequest is req with the run variables a Nudge's text sets.
func nudgedRequest(req LaunchRequest, text string) LaunchRequest {
	env := map[string]string{}
	for k, v := range req.Env {
		env[k] = v
	}
	for _, m := range nudgeEnvLine.FindAllStringSubmatch(text, -1) {
		env[m[1]] = m[2]
	}
	req.Env = env
	return req
}

// playSteps plays steps in order. It reports false once the Session has been
// finished, by a cancel or a failed act.
func (s *fakeSession) playSteps(ctx context.Context, req LaunchRequest, steps []Step) bool {
	for _, st := range steps {
		if st.Pause > 0 {
			select {
			case <-time.After(st.Pause):
			case <-s.cancelCh:
				s.finish(Result{Status: runner.StatusCancelled, ExitStatus: -1})
				return false
			}
		}
		if st.AwaitQueue {
			select {
			case <-s.queueCh:
			case <-s.cancelCh:
				s.finish(Result{Status: runner.StatusCancelled, ExitStatus: -1})
				return false
			}
		}
		if st.Hook != nil {
			s.feed(*st.Hook)
		}
		if st.Act != nil {
			if err := st.Act(ctx, req); err != nil && ctx.Err() == nil {
				s.finish(Result{Status: runner.StatusFailed, ExitStatus: 1, Error: err.Error()})
				return false
			}
		}
		select {
		case <-s.cancelCh:
			s.finish(Result{Status: runner.StatusCancelled, ExitStatus: -1})
			return false
		default:
		}
	}
	return true
}
