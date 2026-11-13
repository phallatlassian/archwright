package runner

import (
	"context"
	"sync"
	"time"
)

// Fake is a Runner that plays a script instead of running an agent.
type Fake struct {
	mu     sync.Mutex
	script Script
}

// Script is what a Fake run plays.
type Script struct {
	Events []Event       // normalized events, after the posture event
	Result Result        // how it ends; Status defaults to done
	Hold   bool          // keep running after the events until cancelled
	Delay  time.Duration // pause before each event
}

// NewFake plays script on every run until SetScript replaces it.
func NewFake(s Script) *Fake { return &Fake{script: s} }

// SetScript replaces what later runs play.
func (f *Fake) SetScript(s Script) {
	f.mu.Lock()
	f.script = s
	f.mu.Unlock()
}

// Name implements Runner.
func (f *Fake) Name() string { return "fake" }

// Capabilities implements Runner.
func (f *Fake) Capabilities() Capabilities {
	return Capabilities{StructuredOutput: true, Usage: true, PermissionDenied: true}
}

// Start implements Runner.
func (f *Fake) Start(ctx context.Context, req StartRequest) (Run, error) {
	if err := Check(req.Spawn); err != nil {
		return nil, err
	}
	f.mu.Lock()
	s := f.script
	f.mu.Unlock()
	mode := req.Spawn.Mode
	if mode == "" {
		mode = "default"
	}
	r := &fakeRun{log: newLog(), done: make(chan struct{}), cancel: make(chan struct{})}
	r.log.add(Event{Kind: KindPosture, Posture: &Posture{
		Runner: "fake", Version: "fake", Mode: mode, Sandbox: "inherited",
		Flags: append([]string{}, req.Spawn.ExtraArgs...), Notices: []string{},
	}})
	go r.play(ctx, s)
	return r, nil
}

type fakeRun struct {
	log    *log
	done   chan struct{}
	cancel chan struct{}
	once   sync.Once
	result Result
}

func (r *fakeRun) Events() <-chan Event { return r.log.subscribe() }

func (r *fakeRun) Result() Result {
	<-r.done
	return r.result
}

func (r *fakeRun) Cancel() { r.once.Do(func() { close(r.cancel) }) }

func (r *fakeRun) play(ctx context.Context, s Script) {
	defer close(r.done)
	defer r.log.close()
	wait := func(d time.Duration) bool { // false once the run is cancelled
		if d <= 0 {
			select {
			case <-r.cancel:
				return false
			case <-ctx.Done():
				return false
			default:
				return true
			}
		}
		select {
		case <-time.After(d):
			return true
		case <-r.cancel:
			return false
		case <-ctx.Done():
			return false
		}
	}
	for _, e := range s.Events {
		if !wait(s.Delay) {
			r.result = Result{Status: StatusCancelled}
			return
		}
		r.log.add(e)
	}
	if s.Hold {
		select {
		case <-r.cancel:
		case <-ctx.Done():
		}
		r.result = Result{Status: StatusCancelled}
		return
	}
	r.result = s.Result
	if r.result.Status == "" {
		r.result.Status = StatusDone
	}
}
