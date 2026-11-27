package works

import (
	"context"
	"fmt"
	"log"
	"time"

	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

// Defaults of the launch stall watch.
const (
	// DefaultStallAfter is how long a launched Session may take to send its
	// SessionStart hook before the developer is told: a CLI at a trust dialog or
	// a permission prompt sends none.
	DefaultStallAfter = 5 * time.Second
	// DefaultFocusIdle is how long the developer's terminal must have had no
	// input before a stalled Session's pane is brought to it.
	DefaultFocusIdle = 5 * time.Second

	stallPoll = 250 * time.Millisecond
)

func stallKey(run string) string { return store.NeedsLaunchStall + ":" + run }

// sessionStarted says whether a launched Session has sent a hook: the same
// evidence the trust probe takes.
func sessionStarted(s hosted.Session) bool {
	st := s.State()
	return (st != hosted.StateStarting && st != hosted.StateEnded) || s.Info().SessionID != ""
}

// watchStall waits for a freshly launched Session to start. One that has not
// within StallAfter is waiting for the developer (a trust dialog, a permission
// prompt) and sends no hook, so the Works has no other signal: Stalled raises
// the Needs-you item, and while the Session is still stalled the Works moves
// tmux focus to its pane once the attached client has been idle for FocusIdle.
// The Works only selects the pane and never sends a key: Enter at a prompt
// approves it (ADR 0027). The item clears when the Session starts or ends.
func (a *SessionRunner) watchStall(ctx context.Context, req RunRequest, s hosted.Session, r hosted.Runner) {
	if a.Stalled == nil {
		return
	}
	after, idle := a.StallAfter, a.FocusIdle
	if after <= 0 {
		after = DefaultStallAfter
	}
	if idle <= 0 {
		idle = DefaultFocusIdle
	}
	begun := time.Now()
	tick := time.NewTicker(stallPoll)
	defer tick.Stop()
	var resolve func()
	focused := false
	defer func() {
		if resolve != nil && !a.detached.Load() {
			resolve()
		}
	}()
	for {
		switch {
		case sessionStarted(s) || s.State() == hosted.StateEnded:
			return
		case ctx.Err() != nil:
			return
		case time.Since(begun) >= after:
			if resolve == nil {
				resolve = a.Stalled(req, s.Info())
			}
			if f, ok := r.(hosted.Focuser); ok && !focused {
				moved, err := f.FocusIfIdle(s.Info().Pane, idle)
				if err != nil {
					log.Printf("run %s: focusing its stalled pane: %v", req.Env.Run, err)
				}
				focused = moved
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// AnnounceStall is the SessionRunner's Stalled hook: it files the Needs-you
// item for a Session that has not started, naming the runner, role, Job,
// window and the exact command that attaches to its pane. One item stands for
// the run; it goes when the Session starts.
func AnnounceStall(st *store.Store) func(RunRequest, hosted.Info) func() {
	return func(req RunRequest, info hosted.Info) func() {
		key := stallKey(req.Env.Run)
		role := req.Env.Role
		attach := paneAttach(req.Job, info.Pane)
		_, err := st.AddNeedsYou(store.NewNeedsYou{
			Kind: store.NeedsLaunchStall, JobID: req.Job, DedupeKey: key,
			Summary: fmt.Sprintf("The %s %s of Job %s has not started: it is probably waiting for you at a dialog (folder trust, a permission prompt). Attach with `%s` and answer it in pane %s of window %s.",
				info.Runner, role, req.Job, attach, info.Pane, req.Job),
			Detail: map[string]string{"runner": info.Runner, "role": role, "guild": req.Guild, "job": req.Job, "window": req.Job, "pane": info.Pane, "attach": attach},
		})
		if err != nil {
			log.Printf("run %s: filing the stalled launch item: %v", req.Env.Run, err)
		}
		return func() {
			if err := st.ResolveNeedsYou(key); err != nil {
				log.Printf("run %s: clearing the stalled launch item: %v", req.Env.Run, err)
			}
		}
	}
}

// envDuration reads a duration such as 5s from the environment; unset or
// invalid means zero, which is the default.
func envDuration(getenv func(string) string, name string) time.Duration {
	d, err := time.ParseDuration(getenv(name))
	if err != nil || d <= 0 {
		return 0
	}
	return d
}
