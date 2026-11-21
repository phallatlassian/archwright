package sites

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// scaffoldDirs are the run scaffolding a Wright's worktree carries; a
// snapshot leaves them out (ADR 0025).
var scaffoldDirs = []string{".agents", ".claude"}

// SnapshotCheckout copies the working tree at worktree, tracked and untracked
// files as they are now, into a throwaway detached checkout outside the Site,
// so a gate run there can never write into the Wright's tree or its git
// directory (ADR 0025). The worktree must be one the Job owns. Release
// removes the checkout.
func (m *Manager) SnapshotCheckout(jobID, worktree string) (*GateCheckout, error) {
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
	var owned bool
	for _, w := range activeOf(wts, "") {
		if w.Path == worktree {
			owned = true
		}
	}
	if !owned {
		return nil, fmt.Errorf("%s is not a worktree of job %s", worktree, jobID)
	}
	head, err := git(worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	branch, _ := git(worktree, "rev-parse", "--abbrev-ref", "HEAD")
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
	if _, err := git(site.ClonePath, "worktree", "add", "--detach", dir, head); err != nil {
		return nil, err
	}
	co := &GateCheckout{Dir: dir, Branch: branch, SHA: head, m: m, site: site}
	if err := mirror(worktree, dir); err != nil {
		_, _ = git(site.ClonePath, "worktree", "remove", "--force", dir)
		return nil, fmt.Errorf("copying the worktree: %w", err)
	}
	return co, nil
}

func isScaffold(rel string) bool {
	for _, d := range scaffoldDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// mirror makes dst's files match src's: tracked and untracked (not ignored)
// files are copied, and files src deleted are removed from dst.
func mirror(src, dst string) error {
	list := func(args ...string) ([]string, error) {
		out, err := git(src, args...)
		if err != nil {
			return nil, err
		}
		var l []string
		for _, p := range strings.Split(out, "\x00") {
			if p != "" && !isScaffold(p) {
				l = append(l, p)
			}
		}
		return l, nil
	}
	present, err := list("ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, rel := range present {
		if _, err := os.Lstat(filepath.Join(src, rel)); err != nil {
			continue // tracked, then deleted
		}
		want[rel] = true
	}
	tracked, err := list("ls-files", "-z", "--cached")
	if err != nil {
		return err
	}
	for _, rel := range tracked {
		if !want[rel] {
			if err := os.RemoveAll(filepath.Join(dst, rel)); err != nil {
				return err
			}
		}
	}
	for rel := range want {
		if err := copyEntry(filepath.Join(src, rel), filepath.Join(dst, rel)); err != nil {
			return err
		}
	}
	return nil
}

func copyEntry(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	_ = os.RemoveAll(to)
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return os.Symlink(target, to)
	case info.Mode().IsRegular():
		in, err := os.Open(from)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return nil // submodule dirs and other special entries are not copied
}
