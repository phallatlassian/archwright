package sites

import (
	"fmt"
	"os"
	"path/filepath"
)

// ProbeDir is the folder the trust probe runs in (docs/spec/tmux-runtime.md):
// the Site's main checkout, the one folder the developer trusts once per
// runner and the one every Job worktree is created from, so they inherit
// trust (ADR 0030). The probe is the only Session ever started there. A Site
// not yet migrated from its bare clone has no main checkout and is probed in
// the folder its Job worktrees sit in, as before.
func (m *Manager) ProbeDir(siteName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, err := m.st.GetSite(siteName)
	if err != nil {
		return "", err
	}
	if !isBare(site) {
		return site.ClonePath, nil
	}
	root, err := m.realRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, site.Name)
	if inside(dir, site.ClonePath) {
		return "", fmt.Errorf("probe directory %s would be inside the clone of site %s", dir, site.Name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
