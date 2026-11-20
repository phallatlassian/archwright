package runner

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorktreeWritableRoots computes the writable roots a Codex Wright needs to
// edit and commit in dir (ADR 0023). For a plain repository it is the repo's
// `.git`. For a linked worktree, whose gitdir sits in the shared clone, it is
// the worktree's own gitdir plus the clone's `objects`, `refs` and `logs`:
// the clone itself is not a root, and `packed-refs` is left out (git's
// non-fatal `packed-refs.lock` message on commit is tolerated). Every root
// exists, is symlink-resolved and passes the guard, or none is returned.
func WorktreeWritableRoots(dir string) ([]string, error) {
	gitdir, err := gitPath(dir, "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	common, err := gitPath(dir, "--git-common-dir")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	for _, p := range []*string{&gitdir, &common} {
		real, err := filepath.EvalSymlinks(*p)
		if err != nil {
			return nil, err
		}
		*p = real
	}
	var roots []string
	if filepath.Clean(common) == filepath.Clean(gitdir) {
		roots = []string{gitdir}
	} else {
		roots = []string{gitdir, filepath.Join(common, "objects"), filepath.Join(common, "refs"), filepath.Join(common, "logs")}
		if rel, err := filepath.Rel(common, gitdir); err != nil || !strings.HasPrefix(rel, "worktrees"+string(filepath.Separator)) {
			return nil, fmt.Errorf("worktree gitdir %s is not under %s/worktrees", gitdir, common)
		}
	}
	if len(roots) == 4 {
		// A bare clone has no logs directory until a ref is first logged, and
		// a root must exist; the shared clone is the Works' own, so make it.
		if err := os.MkdirAll(roots[3], 0o755); err != nil {
			return nil, err
		}
	}
	for i, r := range roots {
		real, err := filepath.EvalSymlinks(r)
		if err != nil {
			return nil, fmt.Errorf("writable root %s: %w", r, err)
		}
		roots[i] = real
	}
	if err := checkWritableRoots(Spawn{WritableRoots: roots}); err != nil {
		return nil, err
	}
	return roots, nil
}

func gitPath(dir string, arg string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", arg)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_DIR=") && !strings.HasPrefix(e, "GIT_WORK_TREE=") && !strings.HasPrefix(e, "GIT_COMMON_DIR=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse %s in %s: %s", arg, dir, strings.TrimSpace(errb.String()+" "+err.Error()))
	}
	return filepath.Clean(strings.TrimSpace(out.String())), nil
}
