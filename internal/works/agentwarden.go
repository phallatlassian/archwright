package works

import (
	"context"
)

// AgentWarden runs task Wardens and review Wardens as real agent processes on
// the vendor CLI their Guild is bound to (ADR 0003), with the same permission
// posture and never-list as Wrights (ADR 0011): it is the AgentRunner behind
// the WardenRunner and ReviewRunner interfaces. The agent works through the
// spool tools like any Warden; this only starts it, mirrors its events to the
// dashboard and turns how it ended into the error the step or review needs.
type AgentWarden struct {
	Agent *AgentRunner
}

// RunWarden implements WardenRunner.
func (w *AgentWarden) RunWarden(ctx context.Context, req WardenRequest) error {
	return w.Agent.run(ctx, RunRequest{Env: req.Env, Job: req.Job, Guild: req.Guild, Piece: req.Step, Worktree: req.Worktree, Brief: req.Brief, ExtraEnv: req.ExtraEnv, Trace: req.Trace}, false)
}

// RunReview implements ReviewRunner, for the Guild that carries out reviews.
func (w *AgentWarden) RunReview(ctx context.Context, req ReviewRequest) error {
	return w.Agent.run(ctx, RunRequest{Env: req.Env, Job: req.Job, Guild: ReviewGuild, Worktree: req.Worktree, Brief: req.Brief, ExtraEnv: req.ExtraEnv, Trace: req.Trace}, false)
}
