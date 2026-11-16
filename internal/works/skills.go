package works

import (
	"fmt"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/store"
)

// SkillsDelivery is how runs get their Guild's skills: dispatched Wrights, and
// the task and review Wardens in the worktree they work in. Without
// it, runs start with none.
type SkillsDelivery struct {
	Catalogue *skills.Catalogue
	Runner    string                               // claude or codex; empty means the runner each Guild is bound to
	RunDir    func(run string) string              // the run's generated files, outside the worktree
	Worktree  func(store.Dispatch) (string, error) // the worktree the dispatch's Wright works in
}

// UseSkills sets the skills delivery for runs.
func (r *Runs) UseSkills(s SkillsDelivery) {
	r.mu.Lock()
	r.skills = &s
	r.mu.Unlock()
}

// deliverSkillsFor gives a dispatch's Wright its Guild's skills before the
// run starts and returns the extra environment the runner needs. A failure,
// including a collision with a skill the Site tracks, fails the dispatch.
func (r *Runs) deliverSkillsFor(d store.Dispatch, run, wt string) (map[string]string, error) {
	return r.deliverGuildSkills(d.Guild, run, wt, func() (string, error) {
		r.mu.Lock()
		sd := r.skills
		r.mu.Unlock()
		return sd.Worktree(d)
	})
}

// deliverGuildSkills gives a run of the Guild its skills in wt, or in the
// worktree the lookup finds when wt is empty, and returns the extra
// environment the runner needs. With no worktree at all nothing is delivered.
func (r *Runs) deliverGuildSkills(guild, run, wt string, lookup func() (string, error)) (map[string]string, error) {
	r.mu.Lock()
	sd, reg, src := r.skills, r.defs, r.guilds
	r.mu.Unlock()
	if sd == nil || sd.Catalogue == nil || (wt == "" && (sd.Worktree == nil || lookup == nil)) || sd.RunDir == nil || reg == nil {
		return nil, nil
	}
	g, ok := reg.Guild(guild)
	if !ok {
		return nil, fmt.Errorf("delivering skills: no guild %q", guild)
	}
	runnerName := sd.Runner
	if runnerName == "" { // the runner the Guild is bound to
		if src == nil {
			return nil, fmt.Errorf("delivering skills: no guilds are configured")
		}
		bound, err := src(guild)
		if err != nil {
			return nil, fmt.Errorf("delivering skills: %w", err)
		}
		runnerName = bound.Binding.Runner
	}
	if wt == "" { // the Wright's own worktree, if it has one
		var err error
		if wt, err = lookup(); err != nil {
			return nil, fmt.Errorf("delivering skills: %w", err)
		}
	}
	return DeliverSkills(sd.Catalogue, g, runnerName, wt, sd.RunDir(run))
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
