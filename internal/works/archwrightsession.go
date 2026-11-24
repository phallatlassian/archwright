package works

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/phallatlassian/archwright/internal/launch"
	hosted "github.com/phallatlassian/archwright/internal/session"
)

// The Archwright as a Session the Works spawns and owns (ADR 0001 as amended by
// ADR 0026): it runs in the `archwright` window of the factory tmux session, in
// the neutral factory home, and the Works wakes it with a Nudge when an item
// reaches its inbox. No hook delivers the inbox: the Session's hooks only
// report its state (ADR 0027).

// MsgArchwrightNotHosted is what the Works says when asked to spawn an
// Archwright Session and it does not host Sessions; the CLI then runs the
// Archwright itself, as before.
const MsgArchwrightNotHosted = "this Works does not host the Archwright as a Session"

// ArchwrightHost is what the Works needs to spawn its Archwright.
type ArchwrightHost struct {
	Runners map[string]hosted.Runner // by the runner name the install's choice gives
	Choice  func() (string, error)   // the install's runner choice, read at each spawn
	Home    string                   // the neutral factory home it runs in
	// SpoolRoot is where the runner's own sandbox can write a run's spool (ADR 0018).
	SpoolRoot func(runner string) string
}

// ArchwrightInfo says where the Archwright's Session lives.
type ArchwrightInfo struct {
	Run     string `json:"run"`
	Runner  string `json:"runner"`
	Pane    string `json:"pane"`
	Window  string `json:"window"`
	Started bool   `json:"started"` // false when it was already open
}

// archSession is the Works' hold on its Archwright Session.
type archSession struct {
	run    string
	runner string
	s      hosted.Session
	kick   chan struct{} // asks the wake loop to look at the inbox
	done   chan struct{} // closed when the Session has ended

	mu     sync.Mutex
	nudged int64 // the newest unread item the Archwright has been nudged about
}

// UseArchwright makes the Works spawn and own its Archwright as a Session.
func (r *Runs) UseArchwright(h *ArchwrightHost) {
	r.archMu.Lock()
	r.archHost = h
	r.archMu.Unlock()
}

// ArchwrightHosted says whether the Works hosts the Archwright as a Session.
func (r *Runs) ArchwrightHosted() bool {
	r.archMu.Lock()
	defer r.archMu.Unlock()
	return r.archHost != nil
}

// StartArchwright spawns the Archwright Session in the `archwright` window, or
// reports the one already open. site is the registered Site the developer
// launched from, "" when there is none; it only matters to a new Session.
func (r *Runs) StartArchwright(site string) (ArchwrightInfo, error) {
	r.archMu.Lock()
	defer r.archMu.Unlock()
	h := r.archHost
	if h == nil {
		return ArchwrightInfo{}, errors.New(MsgArchwrightNotHosted)
	}
	if a := r.archwrightSession(); a != nil { // open: report it; an ended one is replaced
		i := a.s.Info()
		return ArchwrightInfo{Run: a.run, Runner: a.runner, Pane: i.Pane, Window: i.Window}, nil
	}
	choice, err := h.Choice()
	if err != nil {
		return ArchwrightInfo{}, err
	}
	rn, ok := h.Runners[choice]
	if !ok {
		return ArchwrightInfo{}, fmt.Errorf("the %s runner cannot host the Archwright as a Session", choice)
	}
	r.mu.Lock()
	root, closed := r.spoolRoot, r.bg.Err() != nil
	r.mu.Unlock()
	if closed {
		return ArchwrightInfo{}, fmt.Errorf("%w: the Works is shutting down", errInvalidInput)
	}
	if root == "" {
		root = h.SpoolRoot(choice)
	}
	if err := os.MkdirAll(h.Home, 0o700); err != nil {
		return ArchwrightInfo{}, err
	}
	env, err := r.StartFor(RoleArchwright, root, RunContext{})
	if err != nil {
		return ArchwrightInfo{}, err
	}
	vars := map[string]string{}
	for _, kv := range env.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	if site != "" {
		vars[launch.EnvSite] = site
	}
	s, err := rn.Launch(r.bg, hosted.LaunchRequest{Run: env.Run, Role: RoleArchwright, Brief: launch.SessionInstructions(site), Dir: h.Home, Env: vars})
	if err != nil {
		r.End(env.Run)
		return ArchwrightInfo{}, fmt.Errorf("%s was not started: %w", rn.Name(), err) // a never-list refusal fails closed
	}
	a := &archSession{run: env.Run, runner: rn.Name(), s: s, kick: make(chan struct{}, 1), done: make(chan struct{})}
	r.mu.Lock()
	if closed = r.bg.Err() != nil; !closed {
		r.wg.Add(2) // under mu, so Close cannot Wait between the check and the Add
	}
	r.mu.Unlock()
	if closed {
		s.Cancel()
		s.Result()
		r.End(env.Run)
		return ArchwrightInfo{}, fmt.Errorf("%w: the Works is shutting down", errInvalidInput)
	}
	r.setArch(a)
	go r.followArchwright(a)
	go r.wakeLoop(a)
	i := s.Info()
	return ArchwrightInfo{Run: env.Run, Runner: a.runner, Pane: i.Pane, Window: i.Window, Started: true}, nil
}

// followArchwright wakes the Archwright's loop each time it goes idle, and ends
// its run when the Session ends.
func (r *Runs) followArchwright(a *archSession) {
	defer r.wg.Done()
	for e := range a.s.Events() {
		if e.Kind == hosted.KindState && e.State == hosted.StateIdle {
			a.wake()
		}
	}
	a.s.Result()
	close(a.done)
	r.End(a.run)
}

// closeArchwright cancels the Archwright's Session as the Works stops: it
// reports through a spool that goes with the Works, so it is of no use after.
func (r *Runs) closeArchwright() {
	if a := r.archwrightSession(); a != nil {
		a.s.Cancel()
	}
}

func (a *archSession) wake() {
	select {
	case a.kick <- struct{}{}:
	default: // one already pending
	}
}

func (r *Runs) setArch(a *archSession) {
	r.archCurMu.Lock()
	r.arch = a
	r.archCurMu.Unlock()
}

// archwrightSession is the open Archwright Session, nil when there is none. It
// takes only archCurMu, never archMu, which a spawn holds across its launch:
// the inbox notifier calls it on the committing goroutine and must not wait.
func (r *Runs) archwrightSession() *archSession {
	r.archCurMu.Lock()
	defer r.archCurMu.Unlock()
	if a := r.arch; a != nil {
		select {
		case <-a.done:
		default:
			return a
		}
	}
	return nil
}

// wakeLoop nudges the Archwright when its inbox holds an item it has not been
// nudged about and it is idle (ADR 0029).
func (r *Runs) wakeLoop(a *archSession) {
	defer r.wg.Done()
	for {
		select {
		case <-a.done:
			return
		case <-r.bg.Done():
			return
		case <-a.kick:
			r.nudgeArchwright(a)
		}
	}
}

// nudgeArchwright posts the fixed nudge, which never carries item content: the
// inbox stays the source of truth. It sends nothing while the Session is not
// idle, and none before it has finished its first turn (its brief is still its
// first prompt); it is asked again when the Session next goes idle. One Nudge
// is sent per new item, so an Archwright that ends its turn without reading is
// not nudged again until something new arrives.
func (r *Runs) nudgeArchwright(a *archSession) {
	if a.s.Info().Turns == 0 {
		return
	}
	items, err := r.st.Inbox(true)
	if err != nil {
		log.Printf("the Archwright's inbox: %v", err)
		return
	}
	var newest int64
	for _, it := range items {
		newest = max(newest, it.ID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if newest <= a.nudged {
		return
	}
	err = a.s.Nudge(launch.Nudge)
	switch {
	case err == nil:
	case errors.Is(err, hosted.ErrNotIdle):
		return // busy or at a prompt: nothing was sent, and the next idle asks again
	default:
		// The text may be in its composer, so it is not sent again for these items.
		log.Printf("nudging the Archwright session: %v", err)
		if err := r.st.AppendEvent("", "nudge.failed", map[string]string{"error": err.Error()}); err != nil {
			log.Printf("recording the failed nudge: %v", err)
		}
	}
	a.nudged = newest
}
