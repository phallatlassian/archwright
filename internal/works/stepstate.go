package works

import "github.com/phallatlassian/archwright/internal/store"

// Step state labels. The Works does not link a step to its pieces, so a
// step's state is only what the live dispatches of its Guild can tell.
const (
	StepRunning    = "running"
	StepGuildLive  = "a Wright of this Guild is live"
	StepNotTracked = "state not tracked yet"
)

// StepStates labels each Plan step by step ID. A step is running only when a
// live dispatch of its Guild exists and no other step shares that Guild.
func StepStates(steps []store.Step, ds []store.Dispatch) map[string]string {
	live := map[string]bool{}
	for _, d := range ds {
		if d.Status == store.DispatchQueued || d.Status == store.DispatchRunning {
			live[d.Guild] = true
		}
	}
	perGuild := map[string]int{}
	for _, s := range steps {
		perGuild[s.Guild]++
	}
	out := make(map[string]string, len(steps))
	for _, s := range steps {
		switch {
		case !live[s.Guild]:
			out[s.ID] = StepNotTracked
		case perGuild[s.Guild] == 1:
			out[s.ID] = StepRunning
		default:
			out[s.ID] = StepGuildLive
		}
	}
	return out
}
