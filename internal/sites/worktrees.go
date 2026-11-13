package sites

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// jobSite loads an open Job and the Site it belongs to.
func (m *Manager) jobSite(jobID string) (store.Job, store.Site, error) {
	job, err := m.st.GetJob(jobID)
	if err != nil {
		return job, store.Site{}, err
	}
	if job.Site == "" {
		return job, store.Site{}, fmt.Errorf("job %s has no Site", jobID)
	}
	if job.Outcome != "" {
		return job, store.Site{}, fmt.Errorf("job %s is already %s", jobID, job.Outcome)
	}
	site, err := m.st.GetSite(job.Site)
	return job, site, err
}

func activeOf(wts []store.Worktree, kind string) []store.Worktree {
	var out []store.Worktree
	for _, w := range wts {
		if w.RemovedAt == nil && (kind == "" || w.Kind == kind) {
			out = append(out, w)
		}
	}
	return out
}

// CreateWorktree makes a worktree for a Job under the Works' own directory,
// never in the developer's checkout (ADR 0008). Kinds: integration (the
// Job's integration branch), wright (own branch) and readonly (detached).
// baseRef defaults to the Job's integration branch, else the Site's base branch.
// Every branch is new and namespaced by Job, so none can be the default branch.
func (m *Manager) CreateWorktree(jobID, kind, baseRef string) (store.Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if kind != store.KindIntegration && kind != store.KindWright && kind != store.KindReadOnly {
		return store.Worktree{}, fmt.Errorf("kind must be integration, wright or readonly, not %q", kind)
	}
	job, site, err := m.jobSite(jobID)
	if err != nil {
		return store.Worktree{}, err
	}
	existing, err := m.st.Worktrees(jobID)
	if err != nil {
		return store.Worktree{}, err
	}
	integration := activeOf(existing, store.KindIntegration)
	if kind == store.KindIntegration && len(integration) > 0 {
		return store.Worktree{}, fmt.Errorf("job %s already has an integration worktree", jobID)
	}
	if baseRef == "" {
		baseRef = site.BaseBranch
		if kind != store.KindIntegration && len(integration) > 0 {
			baseRef = integration[0].Branch
		}
	}
	if strings.HasPrefix(baseRef, "-") {
		return store.Worktree{}, fmt.Errorf("invalid base ref %q", baseRef)
	}
	sha, err := git(site.Path, "rev-parse", "--verify", "--quiet", baseRef+"^{commit}")
	if err != nil {
		return store.Worktree{}, fmt.Errorf("base ref %q does not resolve to a commit in site %s", baseRef, site.Name)
	}

	id := store.NewWorktreeID()
	wtRoot, err := m.realRoot()
	if err != nil {
		return store.Worktree{}, err
	}
	dir := filepath.Join(wtRoot, site.Name, id)
	if inside(dir, site.Path) {
		return store.Worktree{}, fmt.Errorf("worktree directory %s would be inside site %s", dir, site.Name)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return store.Worktree{}, err
	}
	w := store.Worktree{ID: id, JobID: job.ID, Site: site.Name, Kind: kind, Path: dir, BaseRef: baseRef, BaseSHA: sha}
	switch kind {
	case store.KindReadOnly:
		_, err = git(site.Path, "worktree", "add", "--detach", dir, sha)
	default:
		w.Branch = "archwright/" + job.ID + "/" + id
		if kind == store.KindIntegration {
			w.Branch = "archwright/" + job.ID + "/integration"
		}
		if w.Branch == site.BaseBranch {
			return store.Worktree{}, fmt.Errorf("refusing to use the default branch %q for a factory worktree", w.Branch)
		}
		_, err = git(site.Path, "worktree", "add", "--no-track", "-b", w.Branch, dir, sha)
	}
	if err != nil {
		return store.Worktree{}, err
	}
	rec, err := m.st.AddWorktree(w)
	if err != nil {
		m.remove(site, w, true, true)
		return store.Worktree{}, err
	}
	return rec, nil
}

// remove deletes a worktree directory, optionally force and with its branch,
// and marks it removed. A directory already gone is just pruned.
func (m *Manager) remove(site store.Site, w store.Worktree, force, deleteBranch bool) error {
	if _, err := os.Stat(w.Path); err == nil {
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		if _, err := git(site.Path, append(args, w.Path)...); err != nil {
			return err
		}
	} else if _, err := git(site.Path, "worktree", "prune"); err != nil {
		return err
	}
	if deleteBranch && w.Branch != "" && w.Branch != site.BaseBranch {
		if gitOK(site.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+w.Branch) {
			if _, err := git(site.Path, "branch", "-D", w.Branch); err != nil {
				return err
			}
		}
	}
	return nil
}

// CleanupMerged removes the worktrees (and branches) of writing Wrights whose
// work is already in the Job's integration branch. Everything else is kept
// until the Job is closed. A merged worktree with uncommitted changes is kept.
func (m *Manager) CleanupMerged(jobID string) ([]store.Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, site, err := m.jobSite(jobID)
	if err != nil {
		return nil, err
	}
	wts, err := m.st.Worktrees(jobID)
	if err != nil {
		return nil, err
	}
	removed := []store.Worktree{}
	integration := activeOf(wts, store.KindIntegration)
	if len(integration) == 0 {
		return removed, nil
	}
	var errs []error
	for _, w := range activeOf(wts, store.KindWright) {
		if !merged(site, w, integration[0]) {
			continue
		}
		if err := m.remove(site, w, false, true); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := m.st.MarkWorktreeRemoved(w, "merged"); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, w)
	}
	return removed, errors.Join(errs...)
}

// merged reports whether a Wright has handed back work that is now in the
// integration branch. A branch with no commits of its own has nothing merged
// (it is trivially an ancestor of the branch it was cut from).
func merged(site store.Site, w, integration store.Worktree) bool {
	own, err := git(site.Path, "rev-list", "--count", w.Branch, "--not", w.BaseSHA)
	if err != nil || own == "0" {
		return false
	}
	return gitOK(site.Path, "merge-base", "--is-ancestor", w.Branch, integration.Branch)
}

// Close ends a Job as accepted or discarded and removes every remaining
// worktree. Discarding forces removal and deletes the Job's branches;
// accepting keeps the branches and refuses to throw away uncommitted work,
// leaving the Job open so it can be committed or the Job discarded.
func (m *Manager) Close(jobID, outcome string) ([]store.Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if outcome != "accepted" && outcome != "discarded" {
		return nil, fmt.Errorf("outcome must be accepted or discarded, not %q", outcome)
	}
	_, site, err := m.jobSite(jobID)
	if err != nil {
		return nil, err
	}
	wts, err := m.st.Worktrees(jobID)
	if err != nil {
		return nil, err
	}
	removed := []store.Worktree{}
	var errs []error
	for _, w := range activeOf(wts, "") {
		if err := m.remove(site, w, outcome == "discarded", outcome == "discarded"); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := m.st.MarkWorktreeRemoved(w, outcome); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, w)
	}
	if err := errors.Join(errs...); err != nil {
		return removed, err // job stays open so the rest can be retried
	}
	return removed, m.st.CloseJob(jobID, outcome)
}
