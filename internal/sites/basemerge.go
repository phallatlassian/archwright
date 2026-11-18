package sites

import (
	"fmt"
	"os"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// BaseMerge is what a merge Wright is set up with to bring the Site's base
// branch into a Job's integration branch (ADR 0022).
type BaseMerge struct {
	Worktree string // the Job's integration worktree: the Wright commits on the integration branch itself
	Branch   string // the integration branch
	BaseRef  string // the base branch as the clone knows it, e.g. origin/main
	BaseSHA  string // the commit BaseRef was fetched to
	Tip      string // the integration branch's tip before the merge
}

// PrepareBaseMerge fetches the Site's base branch into the Works' own clone,
// where the Wright's worktree can see it (the Wright is sandboxed and cannot
// fetch), and returns the Job's integration worktree for the Wright to merge
// it in. Merging, not rebasing, keeps the integration branch's history, so
// the Works can push the result as a fast-forward. It refuses an integration
// worktree that has uncommitted changes or is not on the integration branch.
func (m *Manager) PrepareBaseMerge(jobID string) (BaseMerge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, site, err := m.jobSite(jobID)
	if err != nil {
		return BaseMerge{}, err
	}
	wts, err := m.st.Worktrees(jobID)
	if err != nil {
		return BaseMerge{}, err
	}
	integration := activeOf(wts, store.KindIntegration)
	if len(integration) == 0 {
		return BaseMerge{}, fmt.Errorf("job %s has no integration branch to merge into: %w", jobID, store.ErrConflict)
	}
	w := integration[0]
	if _, err := os.Stat(w.Path); err != nil {
		return BaseMerge{}, fmt.Errorf("job %s's integration worktree is missing: %w", jobID, store.ErrConflict)
	}
	if on, err := git(w.Path, "symbolic-ref", "--short", "HEAD"); err != nil || on != w.Branch {
		return BaseMerge{}, fmt.Errorf("job %s's integration worktree is not on %s: %w", jobID, w.Branch, store.ErrConflict)
	}
	if dirty, err := git(w.Path, "status", "--porcelain"); err != nil || dirty != "" {
		return BaseMerge{}, fmt.Errorf("job %s's integration worktree has uncommitted changes or an unfinished merge: %w", jobID, store.ErrConflict)
	}
	if _, err := git(site.ClonePath, "fetch", "--quiet", "--no-tags", "--prune", "origin"); err != nil {
		return BaseMerge{}, fmt.Errorf("fetching site %s: %w", site.Name, err)
	}
	baseSHA, err := git(site.ClonePath, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+site.BaseBranch+"^{commit}")
	if err != nil {
		return BaseMerge{}, fmt.Errorf("base branch %q is not on the remote of site %s", site.BaseBranch, site.Name)
	}
	tip, err := git(site.ClonePath, "rev-parse", "--verify", "--quiet", "refs/heads/"+w.Branch+"^{commit}")
	if err != nil {
		return BaseMerge{}, fmt.Errorf("integration branch %q does not resolve to a commit in site %s", w.Branch, site.Name)
	}
	return BaseMerge{Worktree: w.Path, Branch: w.Branch, BaseRef: "origin/" + site.BaseBranch, BaseSHA: baseSHA, Tip: tip}, nil
}

// IntegrationWorktree is where a Job's integration branch is checked out.
func (m *Manager) IntegrationWorktree(jobID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, _, err := m.jobSite(jobID); err != nil {
		return "", err
	}
	wts, err := m.st.Worktrees(jobID)
	if err != nil {
		return "", err
	}
	integration := activeOf(wts, store.KindIntegration)
	if len(integration) == 0 {
		return "", fmt.Errorf("job %s has no integration worktree: %w", jobID, store.ErrConflict)
	}
	return integration[0].Path, nil
}

// CheckFastForward refuses a push of the Job's integration branch that would
// not be a fast-forward of what the clone last saw on the remote: the branch
// was rewritten (reset, amended or rebased) since. The Works never forces, so
// such a push would only be rejected; this stops it being attempted. With no
// remote-tracking ref (never pushed) there is nothing to rewrite.
func (m *Manager) CheckFastForward(jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, site, err := m.jobSite(jobID)
	if err != nil {
		return err
	}
	wts, err := m.st.Worktrees(jobID)
	if err != nil {
		return err
	}
	integration := activeOf(wts, store.KindIntegration)
	if len(integration) == 0 {
		return nil
	}
	branch := integration[0].Branch
	remote := "refs/remotes/origin/" + branch
	if !gitOK(site.ClonePath, "rev-parse", "--verify", "--quiet", remote+"^{commit}") {
		return nil
	}
	if !gitOK(site.ClonePath, "merge-base", "--is-ancestor", remote, "refs/heads/"+branch) {
		return fmt.Errorf("%s no longer contains what was last pushed to origin, so pushing it would rewrite history; it is not pushed (resolve conflicts by merging the base in, never by rebasing): %w", strings.TrimPrefix(branch, "refs/heads/"), store.ErrConflict)
	}
	return nil
}
