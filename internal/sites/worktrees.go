package sites

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/skills"
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
// from the Works' own clone of the Site, never from the developer's checkout
// (ADR 0008, 0019). Kinds: integration (the Job's integration branch), wright
// (own branch), readonly (detached) and empty (detached with an empty index
// and tree, so git status is quiet). baseRef defaults to the Job's
// integration branch, else the Site's base branch. The clone is fetched first
// and a branch name is resolved against the remote's branches, so a Job starts
// from what the remote has, not from anything local. Every branch is new and
// namespaced by Job, so none can be the default branch.
func (m *Manager) CreateWorktree(jobID, kind, baseRef string) (store.Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if kind != store.KindIntegration && kind != store.KindWright && kind != store.KindReadOnly && kind != store.KindEmpty && kind != store.KindWarden {
		return store.Worktree{}, fmt.Errorf("kind must be integration, wright, warden, readonly or empty, not %q", kind)
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
	fetch := true // the Job's own integration branch lives in the clone; everything else comes from the remote
	if baseRef == "" {
		baseRef = site.BaseBranch
		if kind != store.KindIntegration && len(integration) > 0 {
			baseRef, fetch = integration[0].Branch, false
		}
	}
	if strings.HasPrefix(baseRef, "-") {
		return store.Worktree{}, fmt.Errorf("invalid base ref %q", baseRef)
	}
	if fetch {
		if _, err := git(site.ClonePath, "fetch", "--quiet", "--no-tags", "--prune", "origin"); err != nil {
			return store.Worktree{}, fmt.Errorf("fetching site %s: %w", site.Name, err)
		}
		syncMain(site)
	}
	sha, err := resolveBase(site, baseRef)
	if err != nil {
		return store.Worktree{}, err
	}

	id := store.NewWorktreeID()
	wtRoot, err := m.realRoot()
	if err != nil {
		return store.Worktree{}, err
	}
	dir := filepath.Join(wtRoot, site.Name, id)
	if inside(dir, site.ClonePath) {
		return store.Worktree{}, fmt.Errorf("worktree directory %s would be inside the clone of site %s", dir, site.Name)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return store.Worktree{}, err
	}
	w := store.Worktree{ID: id, JobID: job.ID, Site: site.Name, Kind: kind, Path: dir, BaseRef: baseRef, BaseSHA: sha}
	switch kind {
	case store.KindReadOnly:
		_, err = git(site.ClonePath, "worktree", "add", "--detach", dir, sha)
	case store.KindEmpty:
		err = addEmptyWorktree(site.ClonePath, dir, sha)
	default:
		w.Branch = "archwright/" + job.ID + "/" + id
		if kind == store.KindIntegration {
			w.Branch = "archwright/" + job.ID + "/integration"
		}
		if w.Branch == site.BaseBranch {
			return store.Worktree{}, fmt.Errorf("refusing to use the default branch %q for a factory worktree", w.Branch)
		}
		_, err = git(site.ClonePath, "worktree", "add", "--no-track", "-b", w.Branch, dir, sha)
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

// EnsureIntegration makes the Job's integration worktree, based on the Site's
// base branch, unless it already has one. It is idempotent, so the Works can
// call it before the first Wright is dispatched and again before a piece is
// accepted without any agent running `worktree create`.
func (m *Manager) EnsureIntegration(jobID string) error {
	m.mu.Lock()
	wts, err := m.st.Worktrees(jobID)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if len(activeOf(wts, store.KindIntegration)) > 0 {
		return nil
	}
	if _, err := m.CreateWorktree(jobID, store.KindIntegration, ""); err != nil {
		// lost a race with a concurrent creator: the worktree exists now
		if wts, e := m.st.Worktrees(jobID); e == nil && len(activeOf(wts, store.KindIntegration)) > 0 {
			return nil
		}
		return err
	}
	return nil
}

// resolveBase resolves a base ref to a commit in the Site's clone. A branch
// name means the remote's branch (refs/remotes/origin/<name>); anything else
// (the Job's own factory branches, a SHA or tag) is resolved as given. The
// clone has no local branches of its own (its main checkout is detached), so
// no local branch can answer.
func resolveBase(site store.Site, ref string) (string, error) {
	for _, candidate := range []string{"refs/remotes/origin/" + ref, ref} {
		if sha, err := git(site.ClonePath, "rev-parse", "--verify", "--quiet", candidate+"^{commit}"); err == nil {
			return sha, nil
		}
	}
	return "", fmt.Errorf("base ref %q does not resolve to a commit on the remote of site %s", ref, site.Name)
}

// remove deletes a worktree directory, optionally force and with its branch,
// and marks it removed. A directory already gone is just pruned.
func (m *Manager) remove(site store.Site, w store.Worktree, force, deleteBranch bool) error {
	if _, err := os.Stat(w.Path); err == nil {
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		} else if err := skills.RemoveDelivered(w.Path); err != nil {
			return err // run scaffolding is not work; real changes still stop the removal below
		}
		if _, err := git(site.ClonePath, append(args, w.Path)...); err != nil {
			return err
		}
	} else if _, err := git(site.ClonePath, "worktree", "prune"); err != nil {
		return err
	}
	if deleteBranch && w.Branch != "" && w.Branch != site.BaseBranch {
		if gitOK(site.ClonePath, "rev-parse", "--verify", "--quiet", "refs/heads/"+w.Branch) {
			if _, err := git(site.ClonePath, "branch", "-D", w.Branch); err != nil {
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
	own, err := git(site.ClonePath, "rev-list", "--count", w.Branch, "--not", w.BaseSHA)
	if err != nil || own == "0" {
		return false
	}
	return gitOK(site.ClonePath, "merge-base", "--is-ancestor", w.Branch, integration.Branch)
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

// addEmptyWorktree adds a detached worktree whose index and tree are empty
// yet inherits the main checkout's folder trust. A bare --no-checkout leaves
// every file showing as a staged deletion, so a sparse checkout that
// excludes everything is set and the index is read back from HEAD, which
// marks each entry skip-worktree and keeps git status quiet.
func addEmptyWorktree(clone, dir, sha string) error {
	if _, err := git(clone, "worktree", "add", "--detach", "--no-checkout", dir, sha); err != nil {
		return err
	}
	if _, err := git(dir, "sparse-checkout", "init", "--no-cone"); err != nil {
		return err
	}
	file, err := git(dir, "rev-parse", "--path-format=absolute", "--git-path", "info/sparse-checkout")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(file, []byte("!/*\n"), 0o644); err != nil {
		return err
	}
	_, err = git(dir, "read-tree", "-mu", "HEAD")
	return err
}

// WardenWorktree gives a Warden a worktree of the Site's main checkout of the
// kind a Guild or step declares (ADR 0030). integration is the Job's single
// full worktree, shared by every Warden that asks for it, so the Job has it
// made if need be and it is never created twice. branch is a writing worktree
// on its own branch, detached a read-only full checkout, and empty a
// --no-checkout worktree. Each of the last three is new per call and based on
// the Job's integration branch once it exists.
func (m *Manager) WardenWorktree(jobID string, kind defs.Worktree) (store.Worktree, error) {
	if kind == defs.WorktreeIntegration {
		if err := m.EnsureIntegration(jobID); err != nil {
			return store.Worktree{}, err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		wts, err := m.st.Worktrees(jobID)
		if err != nil {
			return store.Worktree{}, err
		}
		integration := activeOf(wts, store.KindIntegration)
		if len(integration) == 0 {
			return store.Worktree{}, fmt.Errorf("job %s has no integration worktree: %w", jobID, store.ErrConflict)
		}
		return integration[0], nil
	}
	storeKind := map[defs.Worktree]string{defs.WorktreeBranch: store.KindWarden, defs.WorktreeDetached: store.KindReadOnly, defs.WorktreeEmpty: store.KindEmpty}[kind]
	if storeKind == "" {
		return store.Worktree{}, fmt.Errorf("unknown worktree kind %q", kind)
	}
	return m.CreateWorktree(jobID, storeKind, "")
}
