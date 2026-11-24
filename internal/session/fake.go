package session

import (
	"context"
	"fmt"
	"strconv"
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

// Name implements Runner.
func (f *Fake) Name() string { return f.vendor }

// Capabilities implements Runner.
func (f *Fake) Capabilities() Capabilities {
	return Capabilities{Hooks: true, PermissionRequests: true, Queue: f.vendor == "codex"}
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
	s := &fakeSession{core: newCore(""), cancelAct: cancel, cancelCh: make(chan struct{}), queueCh: make(chan string, 8)}
	s.core.setPane("%fake"+strconv.Itoa(n), "@fake")
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
}

// queueingSession is a fakeSession of a vendor that has a queue.
type queueingSession struct{ *fakeSession }

// Queue implements Queuer: a working Session takes the message, any other
// refuses it.
func (s queueingSession) Queue(text string) error {
	if st := s.State(); st != StateWorking {
		return fmt.Errorf("%w: the Session is %s", ErrNotQueueable, st)
	}
	select {
	case s.queueCh <- text:
		return nil
	default:
		return fmt.Errorf("%w: the queue is full", ErrNotQueueable)
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
	if sc.NoStart {
		s.finish(Result{Status: runner.StatusFailed, ExitStatus: -1, Error: "failed to start: no SessionStart hook"})
		return
	}
	for _, st := range sc.Steps {
		if st.Pause > 0 {
			select {
			case <-time.After(st.Pause):
			case <-s.cancelCh:
				s.finish(Result{Status: runner.StatusCancelled, ExitStatus: -1})
				return
			}
		}
		if st.AwaitQueue {
			select {
			case <-s.queueCh:
			case <-s.cancelCh:
				s.finish(Result{Status: runner.StatusCancelled, ExitStatus: -1})
				return
			}
		}
		if st.Hook != nil {
			s.feed(*st.Hook)
		}
		if st.Act != nil {
			if err := st.Act(ctx, req); err != nil && ctx.Err() == nil {
				s.finish(Result{Status: runner.StatusFailed, ExitStatus: 1, Error: err.Error()})
				return
			}
		}
		select {
		case <-s.cancelCh:
			s.finish(Result{Status: runner.StatusCancelled, ExitStatus: -1})
			return
		default:
		}
	}
	if sc.End {
		s.feed(Hook{Event: HookSessionEnd})
		s.finish(Result{Status: runner.StatusDone, ExitStatus: 0})
		return
	}
	<-s.cancelCh
	s.finish(Result{Status: runner.StatusCancelled, ExitStatus: -1})
}
