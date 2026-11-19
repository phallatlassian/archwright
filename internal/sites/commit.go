package sites

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// Committed is the outcome of CommitWorktree.
type Committed struct {
	Branch    string   // the branch committed on
	SHA       string   // the new commit, or the unchanged tip when nothing was committed
	Files     []string // paths in the commit
	Committed bool     // false when nothing was staged, so no commit was made
}

// CommitWorktree stages and commits in a worktree on a Wright's behalf, from
// the Works, because a sandboxed Codex Wright cannot write the worktree's git
// directory (ADR 0022). The worktree must be an active writing worktree of the
// Job (a Wright's own, or the integration one for a merge Wright) and still
// on its recorded branch, never the Site's base branch. paths limits staging
// to those paths relative to the worktree; none stages everything. gitEnv is
// the run's own git environment, so what the Works hid from git for the run
// (delivered skills, ADR 0018) is not staged. Hooks and signing are off: the
// Works holds the developer's credentials and must not run repository code
// with them or wait on a signing prompt.
func (m *Manager) CommitWorktree(jobID, worktree, message string, paths, gitEnvVars []string) (Committed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, site, err := m.jobSite(jobID)
	if err != nil {
		return Committed{}, err
	}
	wts, err := m.st.Worktrees(jobID)
	if err != nil {
		return Committed{}, err
	}
	var w store.Worktree
	for _, c := range activeOf(wts, "") {
		if c.Path == worktree && c.Kind != store.KindReadOnly {
			w = c
		}
	}
	if w.ID == "" {
		return Committed{}, fmt.Errorf("%s is not a writing worktree of job %s: %w", worktree, jobID, store.ErrConflict)
	}
	if _, err := os.Stat(w.Path); err != nil {
		return Committed{}, fmt.Errorf("the worktree of job %s is missing: %w", jobID, store.ErrConflict)
	}
	if w.Branch == "" || w.Branch == site.BaseBranch {
		return Committed{}, fmt.Errorf("refusing to commit on %q: %w", w.Branch, store.ErrConflict)
	}
	if on, err := git(w.Path, "symbolic-ref", "--short", "HEAD"); err != nil || on != w.Branch {
		return Committed{}, fmt.Errorf("the worktree is not on %s, so nothing is committed: %w", w.Branch, store.ErrConflict)
	}
	specs := []string{"-A", "--", "."}
	if len(paths) > 0 {
		specs = []string{"-A", "--"}
		for _, p := range paths {
			spec, err := literalPathspec(p)
			if err != nil {
				return Committed{}, err
			}
			specs = append(specs, spec)
		}
	}
	// -c options come first so that they apply however the run's own git configuration reads.
	gitc := func(args ...string) (string, error) {
		return gitEnv(w.Path, gitEnvVars, append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "core.fsmonitor=false"}, args...)...)
	}
	if _, err := gitc(append([]string{"add"}, specs...)...); err != nil {
		return Committed{}, err
	}
	staged, err := gitc("diff", "--cached", "--name-only", "-z")
	if err != nil {
		return Committed{}, err
	}
	var files []string
	for _, f := range strings.Split(staged, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	merging := false
	if _, err := gitc("rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		merging = true // a merge in progress commits even with nothing else staged
	}
	if len(files) == 0 && !merging {
		tip, err := gitc("rev-parse", "HEAD")
		return Committed{Branch: w.Branch, SHA: tip}, err
	}
	if _, err := gitc("commit", "--no-verify", "--no-edit", "-m", message); err != nil {
		return Committed{}, err
	}
	sha, err := gitc("rev-parse", "HEAD")
	return Committed{Branch: w.Branch, SHA: sha, Files: files, Committed: true}, err
}

// literalPathspec turns a path relative to the worktree into a pathspec that
// git reads literally, refusing one that could leave the worktree.
func literalPathspec(p string) (string, error) {
	if p == "" || strings.ContainsAny(p, "\x00\r\n") || filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q must be a non-empty relative path", p)
	}
	c := filepath.Clean(p)
	if c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the worktree", p)
	}
	return ":(literal)" + c, nil
}
