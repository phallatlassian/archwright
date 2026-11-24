package works

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
// the run's context cancels the Session; returning does not.
type SessionRunner struct {
	Runners map[string]hosted.Runner // by the runner name a Guild binding gives
	Guilds  GuildSource
	// Record keeps where a run's Session lives and what it is doing; nil ignores it.
	Record func(run string, info hosted.Info, state hosted.State)
	// Fallback carries out the runs of a runner that has no Session adapter
	// yet (Codex); nil fails them.
	Fallback Runner
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
	events := live.Events()
	for {
		select {
		case <-ctx.Done():
			live.Cancel()
			live.Result()
			a.record(req, live.Info(), hosted.StateEnded)
			return context.Canceled
		case e, ok := <-events:
			if !ok {
				return a.ended(req, r, live)
			}
			req.Trace.Emit(timelineKind(e), summarizeSession(e), rawOfSession(e))
			if e.Kind == hosted.KindState {
				a.record(req, live.Info(), e.State) // the state this entry reports, so the record never runs ahead of the timeline
				if e.State == hosted.StateIdle && live.Info().Turns > 0 {
					go func() { // the Session stays open, so keep recording its state until it ends
						for e := range events {
							if e.Kind == hosted.KindState {
								a.record(req, live.Info(), e.State)
							}
						}
						a.record(req, live.Info(), hosted.StateEnded)
					}()
					return nil // the Wright finished its turn
				}
			}
		}
	}
}

func (a *SessionRunner) record(req RunRequest, info hosted.Info, state hosted.State) {
	if a.Record != nil {
		a.Record(req.Env.Run, info, state)
	}
}

// ended is how a Session that ended on its own turns into the dispatch's error.
func (a *SessionRunner) ended(req RunRequest, r hosted.Runner, s hosted.Session) error {
	a.record(req, s.Info(), hosted.StateEnded)
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
