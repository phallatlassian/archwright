package works

import (
	"fmt"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/skills"
)

// DeliverSkills gives a run its Guild's skills (ADR 0005, 0018): a per-run
// copy in the worktree, hidden from git. The returned environment must be set
// on the runner process. An error, including a collision with a skill the
// Site tracks, must fail the run: call it before starting the runner and
// return the error from the run, so the dispatch fails with it visible.
func DeliverSkills(cat *skills.Catalogue, g defs.Guild, runner, worktree, runDir string) (map[string]string, error) {
	set, err := cat.Select(g.Skills)
	if err != nil {
		return nil, fmt.Errorf("guild %q: %w", g.Name, err)
	}
	d, err := skills.Deliver(skills.Request{Worktree: worktree, RunDir: runDir, Runner: runner, Skills: set})
	if err != nil {
		return nil, fmt.Errorf("delivering skills for guild %q: %w", g.Name, err)
	}
	return d.Env, nil
}
