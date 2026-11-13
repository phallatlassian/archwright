package sites

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir,
		"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a git repository with one commit on main.
func newRepo(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	dir = filepath.Join(dir, "repo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-m", "init")
	return dir
}

type env struct {
	m    *Manager
	st   *store.Store
	repo string
	root string
}

func setup(t *testing.T) env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db", "archwright.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, _ := filepath.EvalSymlinks(t.TempDir())
	root = filepath.Join(root, "worktrees")
	e := env{m: New(st, root), st: st, repo: newRepo(t), root: root}
	if _, err := e.m.Register(RegisterInput{Name: "demo", Path: e.repo, BaseBranch: "main", Gates: &store.Gates{Test: "go test ./..."}}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e env) job(t *testing.T) store.Job {
	t.Helper()
	j, err := e.st.CreateJobOn("demo", "a job", "")
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestRegisterAndList(t *testing.T) {
	e := setup(t)
	list, err := e.st.ListSites()
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	s := list[0]
	if s.Name != "demo" || s.Path != e.repo || s.BaseBranch != "main" || s.ConcurrencyCap != 3 || s.Gates.Test != "go test ./..." {
		t.Fatalf("%+v", s)
	}
}

func TestRegisterRefusals(t *testing.T) {
	e := setup(t)
	plain := t.TempDir()
	sub := filepath.Join(e.repo, "sub")
	os.Mkdir(sub, 0o755)
	gates := &store.Gates{}
	for name, in := range map[string]RegisterInput{
		"not a repo":         {Name: "x", Path: plain, BaseBranch: "main", Gates: gates},
		"subdirectory":       {Name: "x", Path: sub, BaseBranch: "main", Gates: gates},
		"relative":           {Name: "x", Path: "repo", BaseBranch: "main", Gates: gates},
		"missing branch":     {Name: "x", Path: newRepo(t), BaseBranch: "nope", Gates: gates},
		"missing remote":     {Name: "x", Path: newRepo(t), BaseBranch: "main", Remote: "origin", Gates: gates},
		"unconfirmed":        {Name: "x", Path: newRepo(t), BaseBranch: "main"},
		"duplicate path":     {Name: "other", Path: e.repo, BaseBranch: "main", Gates: gates},
		"duplicate name":     {Name: "demo", Path: newRepo(t), BaseBranch: "main", Gates: gates},
		"bad name":           {Name: "A b", Path: newRepo(t), BaseBranch: "main", Gates: gates},
		"overlaps worktrees": {Name: "x", Path: filepath.Dir(e.root), BaseBranch: "main", Gates: gates},
	} {
		if _, err := e.m.Register(in); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
}

func TestResolveRefusesUnregisteredPaths(t *testing.T) {
	e := setup(t)
	if s, err := e.m.Resolve(e.repo); err != nil || s.Name != "demo" {
		t.Fatalf("%+v %v", s, err)
	}
	sub := filepath.Join(e.repo, "sub")
	os.Mkdir(sub, 0o755)
	if s, err := e.m.Resolve(sub); err != nil || s.Name != "demo" {
		t.Fatalf("subdirectory: %+v %v", s, err)
	}
	for _, p := range []string{t.TempDir(), e.repo + "-sibling", "relative"} {
		if _, err := e.m.Resolve(p); err == nil {
			t.Errorf("%s resolved", p)
		}
	}
	if _, err := e.m.Resolve(t.TempDir()); !errors.Is(err, ErrUnregistered) {
		t.Errorf("want ErrUnregistered, got %v", err)
	}
	// A Job cannot be created on an unregistered Site.
	if _, err := e.st.CreateJobOn("ghost", "x", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("job on unregistered site: %v", err)
	}
}

func TestDetect(t *testing.T) {
	repo := newRepo(t)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644)
	e := setup(t)
	e.m.lookPath = func(string) (string, error) { return "", errors.New("none") }
	p, err := e.m.Detect(repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "repo" || p.BaseBranch != "main" || p.Gates.Test != "go test ./..." || p.ConcurrencyCap != 3 {
		t.Fatalf("%+v", p)
	}
}

func TestDetectGoLintOnlyWhenGolangciLintIsOnPath(t *testing.T) {
	repo := newRepo(t)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644)
	e := setup(t)
	e.m.lookPath = func(string) (string, error) { return "/bin/golangci-lint", nil }
	p, _ := e.m.Detect(repo)
	if p.Gates != (store.Gates{Typecheck: "go vet ./...", Lint: "golangci-lint run", Test: "go test ./..."}) {
		t.Fatalf("with linter: %+v", p.Gates)
	}
	e.m.lookPath = func(string) (string, error) { return "", errors.New("none") }
	if p, _ = e.m.Detect(repo); p.Gates.Lint != "" {
		t.Fatalf("without linter: %+v", p.Gates)
	}
}

func TestDetectReadsMakefileTargets(t *testing.T) {
	const mk = ".PHONY: test lint vet\nCC := gcc\nTEST_FLAGS=-v\ntest:\n\tgo test\nlint: deps\n\ttrue\nvet:\n\ttrue\n\ttestx:\n"
	e := setup(t)
	e.m.lookPath = func(string) (string, error) { return "", errors.New("none") }

	// No marker file: the Makefile is the only source.
	repo := newRepo(t)
	os.WriteFile(filepath.Join(repo, "Makefile"), []byte(mk), 0o644)
	p, _ := e.m.Detect(repo)
	if p.Gates != (store.Gates{Typecheck: "make vet", Lint: "make lint", Test: "make test"}) {
		t.Fatalf("makefile only: %+v", p.Gates)
	}

	// typecheck is preferred over vet.
	os.WriteFile(filepath.Join(repo, "Makefile"), []byte("typecheck:\nvet:\n"), 0o644)
	if p, _ = e.m.Detect(repo); p.Gates.Typecheck != "make typecheck" {
		t.Fatalf("%+v", p.Gates)
	}

	// Variables, .PHONY, indented lines and missing targets propose nothing.
	os.WriteFile(filepath.Join(repo, "Makefile"), []byte(".PHONY: test\ntest := x\n\ttest:\n"), 0o644)
	if p, _ = e.m.Detect(repo); p.Gates != (store.Gates{}) {
		t.Fatalf("%+v", p.Gates)
	}

	// With a marker file, its defaults win and the Makefile fills the gaps.
	repo = newRepo(t)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "Makefile"), []byte(mk), 0o644)
	p, _ = e.m.Detect(repo)
	if p.Gates != (store.Gates{Typecheck: "go vet ./...", Lint: "make lint", Test: "go test ./..."}) {
		t.Fatalf("marker + makefile: %+v", p.Gates)
	}
}

func TestRegisterFillsBlanksFromDetection(t *testing.T) {
	e := setup(t)
	repo := newRepo(t)
	run(t, repo, "remote", "add", "origin", "https://example.com/r.git")
	site, err := e.m.Register(RegisterInput{Path: repo, Gates: &store.Gates{Test: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if site.Name != "repo" || site.BaseBranch != "main" || site.Remote != "origin" || site.Gates.Test != "t" {
		t.Fatalf("%+v", site)
	}
}

func TestRegisterExplicitValuesWinOverDetected(t *testing.T) {
	e := setup(t)
	repo := newRepo(t)
	run(t, repo, "remote", "add", "origin", "https://example.com/r.git")
	run(t, repo, "remote", "add", "up", "https://example.com/u.git")
	run(t, repo, "branch", "dev")
	site, err := e.m.Register(RegisterInput{Name: "mine", Path: repo, BaseBranch: "dev", Remote: "up", Gates: &store.Gates{}})
	if err != nil {
		t.Fatal(err)
	}
	if site.Name != "mine" || site.BaseBranch != "dev" || site.Remote != "up" || site.Gates != (store.Gates{}) {
		t.Fatalf("%+v", site)
	}
}

func TestRegisterStillRequiresGatesAndShowsTheProposal(t *testing.T) {
	e := setup(t)
	repo := newRepo(t)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644)
	_, err := e.m.Register(RegisterInput{Path: repo})
	if err == nil || !strings.Contains(err.Error(), "go vet ./...") || !strings.Contains(err.Error(), "go test ./...") {
		t.Fatalf("want error carrying the detected gates, got %v", err)
	}
	if _, err := e.st.SiteForPath(repo); err == nil {
		t.Fatal("registered without confirmed gates")
	}
}

func TestWorktreesLiveOutsideTheSiteAndKeepDefaultBranchUntouched(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	mainBefore := run(t, e.repo, "rev-parse", "main")

	integ, err := e.m.CreateWorktree(j.ID, store.KindIntegration, "")
	if err != nil {
		t.Fatal(err)
	}
	wright, err := e.m.CreateWorktree(j.ID, store.KindWright, "")
	if err != nil {
		t.Fatal(err)
	}
	ro, err := e.m.CreateWorktree(j.ID, store.KindReadOnly, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []store.Worktree{integ, wright, ro} {
		if inside(w.Path, e.repo) || !inside(w.Path, e.root) {
			t.Errorf("%s worktree at %s", w.Kind, w.Path)
		}
		if _, err := os.Stat(filepath.Join(w.Path, "a.txt")); err != nil {
			t.Errorf("%s worktree not checked out: %v", w.Kind, err)
		}
	}
	// The developer's checkout is untouched: still on main, still clean.
	if b := run(t, e.repo, "symbolic-ref", "--short", "HEAD"); b != "main" {
		t.Errorf("developer checkout moved to %s", b)
	}
	if s := run(t, e.repo, "status", "--porcelain"); s != "" {
		t.Errorf("developer checkout dirty: %s", s)
	}
	// Writing worktrees are on their own branches; read-only is detached.
	if b := run(t, wright.Path, "symbolic-ref", "--short", "HEAD"); b != wright.Branch || b == "main" {
		t.Errorf("wright on %s", b)
	}
	if integ.Branch != "archwright/"+j.ID+"/integration" {
		t.Errorf("integration branch %s", integ.Branch)
	}
	if exec.Command("git", "-C", ro.Path, "symbolic-ref", "-q", "HEAD").Run() == nil {
		t.Error("read-only worktree is not detached")
	}
	// A Wright branches from the integration branch once it exists.
	if wright.BaseRef != integ.Branch {
		t.Errorf("wright base %s", wright.BaseRef)
	}

	// Commits in worktrees never move main, and main is never checked out elsewhere.
	os.WriteFile(filepath.Join(wright.Path, "w.txt"), []byte("w"), 0o644)
	run(t, wright.Path, "add", ".")
	run(t, wright.Path, "commit", "-m", "work")
	if got := run(t, e.repo, "rev-parse", "main"); got != mainBefore {
		t.Errorf("main moved: %s -> %s", mainBefore, got)
	}
	if list := run(t, e.repo, "worktree", "list", "--porcelain"); strings.Count(list, "branch refs/heads/main") != 1 {
		t.Errorf("main checked out in more than one worktree:\n%s", list)
	}
}

func TestWorktreeBaseRefAndGuards(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	run(t, e.repo, "branch", "feature")
	w, err := e.m.CreateWorktree(j.ID, store.KindWright, "feature")
	if err != nil || w.BaseRef != "feature" {
		t.Fatalf("%+v %v", w, err)
	}
	if _, err := e.m.CreateWorktree(j.ID, store.KindWright, "nope"); err == nil {
		t.Error("unknown base ref accepted")
	}
	if _, err := e.m.CreateWorktree(j.ID, store.KindWright, "--upload-pack=x"); err == nil {
		t.Error("option-like base ref accepted")
	}
	if _, err := e.m.CreateWorktree(j.ID, "bogus", ""); err == nil {
		t.Error("bogus kind accepted")
	}
	if _, err := e.m.CreateWorktree(j.ID, store.KindIntegration, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CreateWorktree(j.ID, store.KindIntegration, ""); err == nil {
		t.Error("second integration worktree accepted")
	}
	plain, _ := e.st.CreateJob("no site", "")
	if _, err := e.m.CreateWorktree(plain.ID, store.KindWright, ""); err == nil {
		t.Error("worktree for a Job with no Site")
	}
}

func TestCleanupRemovesOnlyMergedWrights(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	integ, _ := e.m.CreateWorktree(j.ID, store.KindIntegration, "")
	merged, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
	unmerged, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
	ro, _ := e.m.CreateWorktree(j.ID, store.KindReadOnly, "")

	for _, w := range []store.Worktree{merged, unmerged} {
		os.WriteFile(filepath.Join(w.Path, w.ID+".txt"), []byte("x"), 0o644)
		run(t, w.Path, "add", ".")
		run(t, w.Path, "commit", "-m", "work "+w.ID)
	}
	// Merge Wright work into the integration branch.
	run(t, integ.Path, "merge", "--no-ff", "-m", "merge", merged.Branch)

	removed, err := e.m.CleanupMerged(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].ID != merged.ID {
		t.Fatalf("removed %+v", removed)
	}
	for w, wantGone := range map[store.Worktree]bool{merged: true, unmerged: false, integ: false, ro: false} {
		_, err := os.Stat(w.Path)
		if gone := os.IsNotExist(err); gone != wantGone {
			t.Errorf("%s %s: gone=%v want %v", w.Kind, w.ID, gone, wantGone)
		}
	}
	if gitOK(e.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+merged.Branch) {
		t.Error("merged branch kept")
	}
	if !gitOK(e.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+unmerged.Branch) {
		t.Error("unmerged branch deleted")
	}
	again, err := e.m.CleanupMerged(j.ID)
	if err != nil || len(again) != 0 {
		t.Errorf("second cleanup: %v %v", again, err)
	}
}

func TestMergedWorktreeWithUncommittedChangesIsKept(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	integ, _ := e.m.CreateWorktree(j.ID, store.KindIntegration, "")
	w, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
	os.WriteFile(filepath.Join(w.Path, "w.txt"), []byte("w"), 0o644)
	run(t, w.Path, "add", ".")
	run(t, w.Path, "commit", "-m", "work")
	run(t, integ.Path, "merge", "--no-ff", "-m", "merge", w.Branch)
	os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("dirty"), 0o644) // tracked file modified after merge
	removed, err := e.m.CleanupMerged(j.ID)
	if err == nil || len(removed) != 0 {
		t.Fatalf("removed %v err %v", removed, err)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Error("dirty worktree was removed")
	}
}

func TestCloseAcceptKeepsBranchesDiscardDeletesThem(t *testing.T) {
	for _, outcome := range []string{"accepted", "discarded"} {
		t.Run(outcome, func(t *testing.T) {
			e := setup(t)
			j := e.job(t)
			integ, _ := e.m.CreateWorktree(j.ID, store.KindIntegration, "")
			w, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
			ro, _ := e.m.CreateWorktree(j.ID, store.KindReadOnly, "")

			removed, err := e.m.Close(j.ID, outcome)
			if err != nil || len(removed) != 3 {
				t.Fatalf("%v %v", removed, err)
			}
			for _, x := range []store.Worktree{integ, w, ro} {
				if _, err := os.Stat(x.Path); !os.IsNotExist(err) {
					t.Errorf("%s kept", x.Kind)
				}
			}
			wantBranch := outcome == "accepted"
			if got := gitOK(e.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+integ.Branch); got != wantBranch {
				t.Errorf("integration branch present=%v", got)
			}
			if job, _ := e.st.GetJob(j.ID); job.Outcome != outcome {
				t.Errorf("outcome %q", job.Outcome)
			}
			if _, err := e.m.CreateWorktree(j.ID, store.KindWright, ""); err == nil {
				t.Error("worktree created for a closed Job")
			}
			if _, err := e.m.Close(j.ID, outcome); err == nil {
				t.Error("closed twice")
			}
		})
	}
}

func TestAcceptRefusesToDropUncommittedWorkDiscardDoesNot(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	w, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
	os.WriteFile(filepath.Join(w.Path, "dirty.txt"), []byte("x"), 0o644)
	if _, err := e.m.Close(j.ID, "accepted"); err == nil {
		t.Fatal("accepted with uncommitted work")
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Error("dirty worktree removed on accept")
	}
	if job, _ := e.st.GetJob(j.ID); job.Outcome != "" {
		t.Errorf("job closed: %q", job.Outcome)
	}
	if _, err := e.m.Close(j.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Error("worktree kept after discard")
	}
}

func TestFreshWrightIsNotMergedAndNestedSiteResolvesToItself(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	e.m.CreateWorktree(j.ID, store.KindIntegration, "")
	fresh, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
	if removed, err := e.m.CleanupMerged(j.ID); err != nil || len(removed) != 0 {
		t.Fatalf("%v %v", removed, err)
	}
	if _, err := os.Stat(fresh.Path); err != nil {
		t.Error("commit-less Wright removed as merged")
	}

	nested := filepath.Join(e.repo, "inner")
	os.Mkdir(nested, 0o755)
	run(t, nested, "init", "-b", "main")
	run(t, nested, "commit", "--allow-empty", "-m", "i")
	if _, err := e.m.Register(RegisterInput{Name: "inner", Path: nested, BaseBranch: "main", Gates: &store.Gates{}}); err != nil {
		t.Fatal(err)
	}
	if s, err := e.m.Resolve(nested); err != nil || s.Name != "inner" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestCloseToleratesWorktreeDirectoryAlreadyGone(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	w, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
	os.RemoveAll(w.Path)
	if _, err := e.m.Close(j.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
}
