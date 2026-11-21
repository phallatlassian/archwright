package sites

import (
	"fmt"
	"os"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// Outcomes of MergePiece.
const (
	PieceMerged    = "merged"    // the piece branch's commits are now in the integration branch
	PieceUnchanged = "unchanged" // they already were, or the piece made no commits: nothing changed
	PieceConflict  = "conflict"  // the merge conflicts; it was aborted and the integration branch is as it was
)

// PieceMerge is the result of merging a Wright's piece branch into the Job's
// integration branch.
type PieceMerge struct {
	Status    string
	Branch    string   // the integration branch
	Tip       string   // the integration branch's tip before the merge
	Conflicts []string // the files that conflicted, for PieceConflict
}

// MergePiece merges a Wright's piece branch into the Job's integration branch
// when the Warden accepts the piece (ADR 0025): a fast-forward when the
// integration branch has not moved, a merge commit otherwise. A conflict is
// aborted, never left half-merged, so the integration branch and its worktree
// are exactly as they were and the caller can dispatch a merge Wright. The
// branch must be one the Job's Wrights worked on. A merge never rebases,
// resets or rewrites history.
func (m *Manager) MergePiece(jobID, branch string) (PieceMerge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, site, err := m.jobSite(jobID)
	if err != nil {
		return PieceMerge{}, err
	}
	wts, err := m.st.Worktrees(jobID)
	if err != nil {
		return PieceMerge{}, err
	}
	var piece bool
	for _, w := range activeOf(wts, store.KindWright) {
		piece = piece || (w.Branch != "" && w.Branch == branch)
	}
	if !piece {
		return PieceMerge{}, fmt.Errorf("%q is not a Wright branch of job %s", branch, jobID)
	}
	integration := activeOf(wts, store.KindIntegration)
	if len(integration) == 0 {
		return PieceMerge{}, fmt.Errorf("job %s has no integration branch to merge into: %w", jobID, store.ErrConflict)
	}
	w := integration[0]
	if _, err := os.Stat(w.Path); err != nil {
		return PieceMerge{}, fmt.Errorf("job %s's integration worktree is missing: %w", jobID, store.ErrConflict)
	}
	if on, err := git(w.Path, "symbolic-ref", "--short", "HEAD"); err != nil || on != w.Branch {
		return PieceMerge{}, fmt.Errorf("job %s's integration worktree is not on %s: %w", jobID, w.Branch, store.ErrConflict)
	}
	if dirty, err := git(w.Path, "status", "--porcelain"); err != nil || dirty != "" {
		return PieceMerge{}, fmt.Errorf("job %s's integration worktree has uncommitted changes or an unfinished merge: %w", jobID, store.ErrConflict)
	}
	tip, err := git(site.ClonePath, "rev-parse", "--verify", "--quiet", "refs/heads/"+w.Branch+"^{commit}")
	if err != nil {
		return PieceMerge{}, fmt.Errorf("integration branch %q does not resolve to a commit in site %s", w.Branch, site.Name)
	}
	head, err := git(site.ClonePath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return PieceMerge{}, fmt.Errorf("piece branch %q does not resolve to a commit in site %s", branch, site.Name)
	}
	out := PieceMerge{Branch: w.Branch, Tip: tip}
	if gitOK(site.ClonePath, "merge-base", "--is-ancestor", head, tip) {
		out.Status = PieceUnchanged
		return out, nil
	}
	if _, err := git(w.Path,
		"-c", "user.name=Archwright", "-c", "user.email=archwright@localhost", "-c", "commit.gpgsign=false",
		"merge", "--no-edit", "--no-verify", "-m", "merge piece branch "+branch, branch); err != nil {
		files, _ := git(w.Path, "diff", "--name-only", "--diff-filter=U")
		if _, abortErr := git(w.Path, "merge", "--abort"); abortErr != nil {
			return PieceMerge{}, fmt.Errorf("merging %s failed (%v) and could not be aborted: %w", branch, err, abortErr)
		}
		if now, _ := git(w.Path, "rev-parse", "HEAD"); now != tip {
			return PieceMerge{}, fmt.Errorf("merging %s failed (%v) and left %s at %s, not %s", branch, err, w.Branch, now, tip)
		}
		if len(strings.Fields(files)) == 0 { // failed for a reason other than a conflict
			return PieceMerge{}, fmt.Errorf("merging %s into %s: %w", branch, w.Branch, err)
		}
		out.Status, out.Conflicts = PieceConflict, strings.Fields(files)
		return out, nil
	}
	out.Status = PieceMerged
	return out, nil
}
