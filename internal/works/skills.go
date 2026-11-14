package works

import (
	"fmt"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/store"
)

// SkillsDelivery is how dispatched Wrights get their Guild's skills. Without
// it, runs start with none.
type SkillsDelivery struct {
	Catalogue *skills.Catalogue
	Runner    string                               // claude or codex
	RunDir    func(run string) string              // the run's generated files, outside the worktree
	Worktree  func(store.Dispatch) (string, error) // the worktree the dispatch's Wright works in
}

// UseSkills sets the skills delivery for dispatched Wrights.
func (r *Runs) UseSkills(s SkillsDelivery) {
	r.mu.Lock()
	r.skills = &s
	r.mu.Unlock()
}

// deliverSkillsFor gives a dispatch's Wright its Guild's skills before the
// run starts and returns the extra environment the runner needs. A failure,
// including a collision with a skill the Site tracks, fails the dispatch.
func (r *Runs) deliverSkillsFor(d store.Dispatch, run string) (map[string]string, error) {
	r.mu.Lock()
	sd, reg := r.skills, r.defs
	r.mu.Unlock()
	if sd == nil || sd.Catalogue == nil || sd.Worktree == nil || sd.RunDir == nil || reg == nil {
		return nil, nil
	}
	g, ok := reg.Guild(d.Guild)
	if !ok {
		return nil, fmt.Errorf("delivering skills: no guild %q", d.Guild)
	}
	wt, err := sd.Worktree(d)
	if err != nil {
		return nil, fmt.Errorf("delivering skills: %w", err)
	}
	return DeliverSkills(sd.Catalogue, g, sd.Runner, wt, sd.RunDir(run))
}

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
