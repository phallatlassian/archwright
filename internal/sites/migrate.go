package sites

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// BlockedError is why a Site's bare clone was not migrated: Jobs are active,
// so worktrees of the bare clone may be in use (ADR 0030).
type BlockedError struct {
	Site string
	Jobs []string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("site %s still has active Jobs (%s); its bare clone is migrated once they close", e.Site, strings.Join(e.Jobs, ", "))
}

// isBare reports whether the Site's clone is a bare repository: the clone
// layout before ADR 0030. A clone that is missing is not.
func isBare(site store.Site) bool {
	if _, err := os.Stat(site.ClonePath); err != nil {
		return false
	}
	out, err := git(site.ClonePath, "rev-parse", "--is-bare-repository")
	return err == nil && out == "true"
}

// NeedsMigration reports whether the Site still has the bare clone that ADR
// 0030 replaces with a normal clone and a main checkout.
func (m *Manager) NeedsMigration(name string) (bool, error) {
	site, err := m.st.GetSite(name)
	if err != nil {
		return false, err
	}
	return isBare(site), nil
}

// Migrate replaces a Site's bare clone with a normal clone with a main
// checkout at <clones>/<site>/main, keeping the Site's Jobs and their history
// (they are rows of the registry, not of the clone), and deletes the bare
// clone. It reports whether it migrated. A Site that has no bare clone is left
// alone; one with an active Job is refused with a *BlockedError, because a
// Job's worktrees belong to the clone they were made from. The caller runs the
// trust probe in the new main checkout.
func (m *Manager) Migrate(name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, err := m.st.GetSite(name)
	if err != nil {
		return false, err
	}
	if !isBare(site) {
		return false, nil
	}
	if !inside(site.ClonePath, m.clones) || site.ClonePath == m.clones {
		return false, fmt.Errorf("site %s: its clone %s is outside %s, so it is left alone", name, site.ClonePath, m.clones)
	}
	active, err := m.st.ActiveJobs(name)
	if err != nil {
		return false, err
	}
	if len(active) > 0 {
		return false, &BlockedError{Site: name, Jobs: active}
	}
	dir := mainDir(m.clones, name)
	if err := os.RemoveAll(filepath.Dir(dir)); err != nil { // debris of an earlier attempt: ours
		return false, err
	}
	if err := cloneMain(dir, site.URL, site.BaseBranch); err != nil {
		os.RemoveAll(filepath.Dir(dir))
		return false, err
	}
	if err := m.st.SetClonePath(name, dir); err != nil {
		os.RemoveAll(filepath.Dir(dir))
		return false, err
	}
	if err := os.RemoveAll(site.ClonePath); err != nil {
		return true, fmt.Errorf("site %s migrated, but deleting its old bare clone %s failed: %w", name, site.ClonePath, err)
	}
	return true, nil
}

// IsBlocked reports whether err is a *BlockedError.
func IsBlocked(err error) bool {
	var b *BlockedError
	return errors.As(err, &b)
}
