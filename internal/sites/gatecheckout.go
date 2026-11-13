package sites

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/phallatlassian/archwright/internal/store"
)

// GateCheckout is a throwaway, detached checkout of a Job's integration
// branch where the Site's gates run.
type GateCheckout struct {
	Dir    string
	Branch string
	SHA    string
	m      *Manager
	site   store.Site
}

// GateCheckout checks out the tip of the Job's integration branch, the branch
// a handback hands back, into a detached worktree outside the Site. Gates
// never run in a Wright's own worktree, whose uncommitted files are not what
// was handed back. Release removes it.
func (m *Manager) GateCheckout(jobID string) (*GateCheckout, error) {
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
	integration := activeOf(wts, store.KindIntegration)
	if len(integration) == 0 {
		return nil, fmt.Errorf("job %s has no integration branch to run gates on", jobID)
	}
	branch := integration[0].Branch
	sha, err := git(site.ClonePath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("integration branch %q does not resolve to a commit in site %s", branch, site.Name)
	}
	wtRoot, err := m.realRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(wtRoot, site.Name, store.NewWorktreeID()+"-gates")
	if inside(dir, site.ClonePath) {
		return nil, fmt.Errorf("worktree directory %s would be inside site %s", dir, site.Name)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if _, err := git(site.ClonePath, "worktree", "add", "--detach", dir, sha); err != nil {
		return nil, err
	}
	return &GateCheckout{Dir: dir, Branch: branch, SHA: sha, m: m, site: site}, nil
}

// Release removes the checkout, whatever the gates left in it.
func (c *GateCheckout) Release() error {
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	_, err := git(c.site.ClonePath, "worktree", "remove", "--force", c.Dir)
	return err
}
