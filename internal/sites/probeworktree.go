package sites

import (
	"fmt"
	"os"
	"path/filepath"
)

// ProbeDir is the folder the trust probe runs in (docs/spec/tmux-runtime.md):
// the Site's folder under the worktree root, the parent of every Job's
// worktree of the Site, so trust given there is the trust a later Session in
// one of them is covered by wherever a CLI inherits trust from a parent folder.
// It is a plain folder, not a worktree, and stays: nothing in it needs cleaning.
func (m *Manager) ProbeDir(siteName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, err := m.st.GetSite(siteName)
	if err != nil {
		return "", err
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
