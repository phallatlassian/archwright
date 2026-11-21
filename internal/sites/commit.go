package sites

import (
	"fmt"
	"strings"
)

// CommitWorktree commits everything a Wright left in its worktree, tracked
// and untracked, to the worktree's branch with the message the Works built
// (ADR 0025). The Wright never runs git add or commit. Run scaffolding
// (.agents, .claude) is never staged, nor is anything the Site's own ignore
// rules exclude. It returns the new commit, or "" when there was nothing to
// commit: no empty commit is made. The worktree must be one the Job owns and
// have a branch checked out.
func (m *Manager) CommitWorktree(jobID, worktree, message string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, _, err := m.jobSite(jobID); err != nil {
		return "", err
	}
	wts, err := m.st.Worktrees(jobID)
	if err != nil {
		return "", err
	}
	var owned bool
	for _, w := range activeOf(wts, "") {
		if w.Path == worktree && w.Branch != "" {
			owned = true
		}
	}
	if !owned {
		return "", fmt.Errorf("%s is not a branch worktree of job %s", worktree, jobID)
	}
	if _, err := git(worktree, "add", "--all", "--", ".", ":(exclude).agents", ":(exclude).claude"); err != nil {
		return "", err
	}
	// the exclusions leave a Site's own tracked scaffolding as it is; with nothing else staged there is nothing to commit
	if _, err := git(worktree, "diff", "--cached", "--quiet"); err == nil {
		return "", nil
	}
	if _, err := git(worktree,
		"-c", "user.name=Archwright", "-c", "user.email=archwright@localhost",
		"-c", "commit.gpgsign=false", "commit", "--no-verify", "--quiet", "-m", message); err != nil {
		return "", err
	}
	sha, err := git(worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}
