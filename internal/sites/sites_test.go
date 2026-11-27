package sites

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/store"
)

const (
	host    = "https://git.example.com/"
	repoURL = host + "o/r.git"
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

func realTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// remotes is a directory of bare repositories standing in for a git host.
// Git's own url.<base>.insteadOf maps https://git.example.com/ onto it, so
// the code under test sees a real remote URL and never a local path.
type remotes struct {
	dir string
}

func newRemotes(t *testing.T) remotes {
	t.Helper()
	r := remotes{dir: filepath.Join(realTemp(t), "host")}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+r.dir+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", host)
	return r
}

// repo is a publisher's working copy of a remote repository.
type repo struct {
	t    *testing.T
	work string // working copy that pushes to the remote
	bare string // the remote itself
	url  string
}

// create makes remote repository path (like "o/r") with files on main.
func (r remotes) create(t *testing.T, path string, files map[string]string) repo {
	t.Helper()
	rp := repo{t: t, work: filepath.Join(realTemp(t), "publisher"), bare: filepath.Join(r.dir, path+".git"), url: host + path + ".git"}
	if err := os.MkdirAll(rp.work, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, rp.work, "init", "-b", "main")
	if err := os.MkdirAll(filepath.Dir(rp.bare), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, rp.work, "init", "--bare", "-b", "main", rp.bare)
	rp.commit("init", files)
	return rp
}

// commit writes files, commits them on the publisher's current branch and pushes.
func (r repo) commit(msg string, files map[string]string) {
	r.t.Helper()
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(r.work, name)), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.work, name), []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	run(r.t, r.work, "add", ".")
	run(r.t, r.work, "commit", "--allow-empty", "-m", msg)
	run(r.t, r.work, "push", r.bare, "HEAD")
}

func (r repo) branch(name string) {
	r.t.Helper()
	run(r.t, r.work, "checkout", "-b", name)
	run(r.t, r.work, "push", r.bare, name)
	run(r.t, r.work, "checkout", "main")
}

// develop makes a developer's checkout of the remote, as a developer would.
func (r repo) develop() string {
	r.t.Helper()
	dev := filepath.Join(realTemp(r.t), "dev")
	run(r.t, filepath.Dir(dev), "clone", "--quiet", r.url, dev)
	return dev
}

type env struct {
	m      *Manager
	st     *store.Store
	hosts  remotes
	remote repo
	dev    string // the developer's checkout of the remote
	root   string // worktrees
	clones string
}

func setup(t *testing.T) env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db", "archwright.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	state := realTemp(t)
	e := env{st: st, root: filepath.Join(state, "worktrees"), clones: filepath.Join(state, "clones")}
	e.m = New(st, e.root, e.clones)
	e.m.lookPath = func(string) (string, error) { return "", errors.New("none") }
	e.hosts = newRemotes(t)
	e.remote = e.hosts.create(t, "o/r", map[string]string{"a.txt": "a"})
	e.dev = e.remote.develop()
	if _, err := e.m.Register(RegisterInput{Name: "demo", URL: e.remote.url, BaseBranch: "main", Gates: &store.Gates{Test: "go test ./..."}}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e env) clone() string { return mainDir(e.clones, "demo") }

func (e env) job(t *testing.T) store.Job {
	t.Helper()
	j, err := e.st.CreateJobOn("demo", "a job", "")
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func (e env) assertNoClonesOrSites(t *testing.T, what string) {
	t.Helper()
	if list, _ := e.st.ListSites(); len(list) != 1 { // only the fixture's "demo"
		t.Errorf("%s: sites %+v", what, list)
	}
	entries, _ := os.ReadDir(e.clones)
	if len(entries) != 1 || entries[0].Name() != "demo" {
		t.Errorf("%s: clones directory holds %v", what, entries)
	}
}

func TestRegisterMakesANormalCloneWithADetachedMainCheckoutAndRecordsURL(t *testing.T) {
	e := setup(t)
	list, err := e.st.ListSites()
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	s := list[0]
	if s.Name != "demo" || s.URL != e.remote.url || s.ClonePath != e.clone() || s.BaseBranch != "main" || s.ConcurrencyCap != 3 || s.Gates.Test != "go test ./..." {
		t.Fatalf("%+v", s)
	}
	if got := run(t, s.ClonePath, "rev-parse", "--is-bare-repository"); got != "false" {
		t.Errorf("clone is bare: %s", got)
	}
	if _, err := os.Stat(filepath.Join(s.ClonePath, "a.txt")); err != nil {
		t.Errorf("main checkout has no files: %v", err)
	}
	want0 := run(t, e.remote.bare, "rev-parse", "main")
	if got := run(t, s.ClonePath, "rev-parse", "HEAD"); got != want0 {
		t.Errorf("main checkout at %s, want %s", got, want0)
	}
	if exec.Command("git", "-C", s.ClonePath, "symbolic-ref", "-q", "HEAD").Run() == nil {
		t.Error("main checkout is on a branch, not detached")
	}
	if got := run(t, s.ClonePath, "for-each-ref", "refs/heads"); got != "" {
		t.Errorf("clone has local branches: %s", got)
	}
	if !inside(s.ClonePath, e.clones) || inside(s.ClonePath, e.dev) {
		t.Errorf("clone at %s", s.ClonePath)
	}
	want := run(t, e.remote.bare, "rev-parse", "main")
	if got := run(t, s.ClonePath, "rev-parse", "refs/remotes/origin/main"); got != want {
		t.Errorf("origin/main %s, remote main %s", got, want)
	}
	if got := run(t, s.ClonePath, "config", "--get", "remote.origin.url"); got != e.remote.url {
		t.Errorf("origin %s", got)
	}
}

func TestRegisterRefusals(t *testing.T) {
	e := setup(t)
	e.hosts.create(t, "o/two", map[string]string{"x": "x"})
	gates := &store.Gates{}
	for name, in := range map[string]RegisterInput{
		"local path":           {Name: "x", URL: e.dev, BaseBranch: "main", Gates: gates},
		"remote's own path":    {Name: "x", URL: e.remote.bare, BaseBranch: "main", Gates: gates},
		"file url":             {Name: "x", URL: "file://" + e.remote.bare, BaseBranch: "main", Gates: gates},
		"relative path":        {Name: "x", URL: "repo", BaseBranch: "main", Gates: gates},
		"empty":                {Name: "x", BaseBranch: "main", Gates: gates},
		"unreachable":          {Name: "x", URL: host + "o/missing.git", BaseBranch: "main", Gates: gates},
		"missing branch":       {Name: "x", URL: host + "o/two.git", BaseBranch: "nope", Gates: gates},
		"option-like branch":   {Name: "x", URL: host + "o/two.git", BaseBranch: "--upload-pack=x", Gates: gates},
		"unconfirmed":          {Name: "x", URL: host + "o/two.git", BaseBranch: "main"},
		"duplicate url":        {Name: "other", URL: e.remote.url, BaseBranch: "main", Gates: gates},
		"duplicate https":      {Name: "other", URL: strings.TrimSuffix(e.remote.url, ".git"), BaseBranch: "main", Gates: gates},
		"duplicate ssh":        {Name: "other", URL: "git@git.example.com:o/r.git", BaseBranch: "main", Gates: gates},
		"duplicate name":       {Name: "demo", URL: host + "o/two.git", BaseBranch: "main", Gates: gates},
		"bad name":             {Name: "A b", URL: host + "o/two.git", BaseBranch: "main", Gates: gates},
		"name escaping clones": {Name: "../x", URL: host + "o/two.git", BaseBranch: "main", Gates: gates},
	} {
		if _, err := e.m.Register(in); err == nil {
			t.Errorf("%s: registered", name)
		}
		e.assertNoClonesOrSites(t, name)
	}
	if _, err := e.m.Register(RegisterInput{Name: "x", URL: host + "o/two.git", BaseBranch: "main", Gates: gates}); err != nil {
		t.Fatalf("a later valid registration must still work: %v", err)
	}
}

func TestRegisterAndDetectRefuseCredentialURLsAndPersistNothing(t *testing.T) {
	e := setup(t)
	e.hosts.create(t, "o/two", map[string]string{"x": "x"})
	for _, u := range []string{"https://user:s3cret@git.example.com/o/two.git", "https://s3cret@git.example.com/o/two.git", "ssh://git:s3cret@git.example.com/o/two.git"} {
		_, err := e.m.Register(RegisterInput{Name: "x", URL: u, BaseBranch: "main", Gates: &store.Gates{}})
		if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "credentials") {
			t.Errorf("%s: %v", u, err)
		}
		if _, err := e.m.Detect(u); err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("detect %s: %v", u, err)
		}
		e.assertNoClonesOrSites(t, u)
	}
}

func TestRegisterAcceptsPlainUsernameURLs(t *testing.T) {
	e := setup(t)
	e.hosts.create(t, "o/two", map[string]string{"x": "x"})
	if _, err := e.m.Register(RegisterInput{Name: "x", URL: host + "o/two.git", BaseBranch: "main", Gates: &store.Gates{}}); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterLocalPathsExplainWhy(t *testing.T) {
	e := setup(t)
	_, err := e.m.Register(RegisterInput{URL: e.dev, Gates: &store.Gates{}})
	if err == nil || !strings.Contains(err.Error(), "remote URL") {
		t.Fatalf("want an explanation, got %v", err)
	}
}

func TestResolveMatchesOriginURL(t *testing.T) {
	e := setup(t)
	if s, err := e.m.Resolve(e.dev); err != nil || s.Name != "demo" {
		t.Fatalf("%+v %v", s, err)
	}
	sub := filepath.Join(e.dev, "sub")
	os.Mkdir(sub, 0o755)
	if s, err := e.m.Resolve(sub); err != nil || s.Name != "demo" {
		t.Fatalf("subdirectory: %+v %v", s, err)
	}
	// Any spelling of the same remote resolves.
	for _, spelling := range []string{"git@git.example.com:o/r.git", "https://git.example.com/o/r", "ssh://git@GIT.example.com/o/r.git"} {
		other := filepath.Join(realTemp(t), "other")
		run(t, filepath.Dir(other), "init", "-b", "main", other)
		run(t, other, "remote", "add", "origin", spelling)
		if s, err := e.m.Resolve(other); err != nil || s.Name != "demo" {
			t.Errorf("%s: %+v %v", spelling, s, err)
		}
	}
	// Refused: no origin, an origin nobody registered, a local-path origin,
	// a plain directory, a missing one, a relative one.
	noOrigin := filepath.Join(realTemp(t), "none")
	run(t, filepath.Dir(noOrigin), "init", "-b", "main", noOrigin)
	unknown := filepath.Join(realTemp(t), "unknown")
	run(t, filepath.Dir(unknown), "init", "-b", "main", unknown)
	run(t, unknown, "remote", "add", "origin", "https://git.example.com/o/unknown.git")
	local := filepath.Join(realTemp(t), "local")
	run(t, filepath.Dir(local), "init", "-b", "main", local)
	run(t, local, "remote", "add", "origin", e.remote.bare)
	for name, p := range map[string]string{
		"no origin": noOrigin, "unknown origin": unknown, "local-path origin": local,
		"plain directory": t.TempDir(), "missing": e.dev + "-missing", "relative": "relative",
	} {
		if _, err := e.m.Resolve(p); err == nil {
			t.Errorf("%s resolved", name)
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

func TestDetectReadsTheRemoteBaseBranchAndLeavesNoCloneBehind(t *testing.T) {
	e := setup(t)
	r := e.hosts.create(t, "o/proj", map[string]string{"go.mod": "module x\n"})
	before, _ := os.ReadDir(e.clones)

	p, err := e.m.Detect(r.url)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "proj" || p.URL != r.url || p.BaseBranch != "main" || p.ConcurrencyCap != 3 ||
		p.Gates != (store.Gates{Typecheck: "go vet ./...", Test: "go test ./..."}) {
		t.Fatalf("%+v", p)
	}
	if after, _ := os.ReadDir(e.clones); len(after) != len(before) {
		t.Errorf("detect left %v behind (was %v)", after, before)
	}
	if list, _ := e.st.ListSites(); len(list) != 1 {
		t.Errorf("detect registered something: %+v", list)
	}
	for _, bad := range []string{e.dev, "file://" + r.bare, host + "o/missing.git"} {
		if _, err := e.m.Detect(bad); err == nil {
			t.Errorf("detected %s", bad)
		}
	}
	if after, _ := os.ReadDir(e.clones); len(after) != len(before) {
		t.Errorf("failed detect left %v behind", after)
	}

	// Gates come from the remote's base branch, not from another branch.
	r.branch("dev")
	run(t, r.work, "checkout", "dev")
	r.commit("make", map[string]string{"Makefile": "lint:\n"})
	run(t, r.work, "checkout", "main")
	p, err = e.m.detect(r.url, "dev")
	if err != nil || p.Gates.Lint != "make lint" {
		t.Fatalf("dev branch: %+v %v", p, err)
	}
	if _, err := e.m.detect(r.url, "nope"); err == nil {
		t.Error("detected a missing branch")
	}
}

func TestDetectGoLintOnlyWhenGolangciLintIsOnPath(t *testing.T) {
	e := setup(t)
	r := e.hosts.create(t, "o/go", map[string]string{"go.mod": "module x\n"})
	e.m.lookPath = func(string) (string, error) { return "/bin/golangci-lint", nil }
	p, err := e.m.Detect(r.url)
	if err != nil {
		t.Fatal(err)
	}
	if p.Gates != (store.Gates{Typecheck: "go vet ./...", Lint: "golangci-lint run", Test: "go test ./..."}) {
		t.Fatalf("with linter: %+v", p.Gates)
	}
	e.m.lookPath = func(string) (string, error) { return "", errors.New("none") }
	if p, _ = e.m.Detect(r.url); p.Gates.Lint != "" {
		t.Fatalf("without linter: %+v", p.Gates)
	}
}

func TestDetectReadsMakefileTargets(t *testing.T) {
	const mk = ".PHONY: test lint vet\nCC := gcc\nTEST_FLAGS=-v\ntest:\n\tgo test\nlint: deps\n\ttrue\nvet:\n\ttrue\n\ttestx:\n"
	e := setup(t)
	detect := func(name string, files map[string]string) store.Gates {
		t.Helper()
		p, err := e.m.Detect(e.hosts.create(t, "o/"+name, files).url)
		if err != nil {
			t.Fatal(err)
		}
		return p.Gates
	}

	// No marker file: the Makefile is the only source.
	if g := detect("mk1", map[string]string{"Makefile": mk}); g != (store.Gates{Typecheck: "make vet", Lint: "make lint", Test: "make test"}) {
		t.Fatalf("makefile only: %+v", g)
	}
	// typecheck is preferred over vet.
	if g := detect("mk2", map[string]string{"Makefile": "typecheck:\nvet:\n"}); g.Typecheck != "make typecheck" {
		t.Fatalf("%+v", g)
	}
	// Variables, .PHONY, indented lines and missing targets propose nothing.
	if g := detect("mk3", map[string]string{"Makefile": ".PHONY: test\ntest := x\n\ttest:\n"}); g != (store.Gates{}) {
		t.Fatalf("%+v", g)
	}
	// With a marker file, its defaults win and the Makefile fills the gaps.
	if g := detect("mk4", map[string]string{"go.mod": "module x\n", "Makefile": mk}); g != (store.Gates{Typecheck: "go vet ./...", Lint: "make lint", Test: "go test ./..."}) {
		t.Fatalf("marker + makefile: %+v", g)
	}
}

func TestRegisterFillsBlanksFromTheRemote(t *testing.T) {
	e := setup(t)
	r := e.hosts.create(t, "o/My_Repo", map[string]string{"a": "a"})
	site, err := e.m.Register(RegisterInput{URL: r.url, Gates: &store.Gates{Test: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if site.Name != "my_repo" || site.BaseBranch != "main" || site.URL != r.url || site.Gates.Test != "t" {
		t.Fatalf("%+v", site)
	}
}

func TestRegisterExplicitValuesWinOverDetected(t *testing.T) {
	e := setup(t)
	r := e.hosts.create(t, "o/x", map[string]string{"a": "a"})
	r.branch("dev")
	site, err := e.m.Register(RegisterInput{Name: "mine", URL: r.url, BaseBranch: "dev", ConcurrencyCap: 2, Gates: &store.Gates{}})
	if err != nil {
		t.Fatal(err)
	}
	if site.Name != "mine" || site.BaseBranch != "dev" || site.ConcurrencyCap != 2 || site.Gates != (store.Gates{}) {
		t.Fatalf("%+v", site)
	}
}

func TestRegisterStillRequiresGatesAndShowsTheProposal(t *testing.T) {
	e := setup(t)
	r := e.hosts.create(t, "o/gated", map[string]string{"go.mod": "module x\n"})
	_, err := e.m.Register(RegisterInput{URL: r.url})
	if err == nil || !strings.Contains(err.Error(), "go vet ./...") || !strings.Contains(err.Error(), "go test ./...") {
		t.Fatalf("want error carrying the detected gates, got %v", err)
	}
	if _, err := e.st.SiteForURL(r.url); err == nil {
		t.Fatal("registered without confirmed gates")
	}
	e.assertNoClonesOrSites(t, "unconfirmed gates")
}

// tree hashes every file under dir (names and contents), so any write inside
// a developer's checkout, .git included, shows up.
func tree(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintln(h, rel, d.Type())
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestDevelopersCheckoutIsNeverTouched(t *testing.T) {
	remote := newRemotes(t).create(t, "o/r", map[string]string{"a.txt": "a"})
	dev := remote.develop()
	// The developer has unpushed work on main and a local branch.
	os.WriteFile(filepath.Join(dev, "local.txt"), []byte("l"), 0o644)
	run(t, dev, "add", ".")
	run(t, dev, "commit", "-m", "unpushed")
	run(t, dev, "branch", "wip")
	os.WriteFile(filepath.Join(dev, "dirty.txt"), []byte("d"), 0o644) // uncommitted

	snapshot := func() string {
		return strings.Join([]string{
			tree(t, dev),
			run(t, dev, "for-each-ref"),
			run(t, dev, "worktree", "list", "--porcelain"),
			run(t, dev, "config", "--local", "--list"),
			run(t, dev, "status", "--porcelain"),
		}, "\n--\n")
	}
	run(t, dev, "status") // settle the index before snapshotting
	before := snapshot()

	st, err := store.Open(filepath.Join(t.TempDir(), "db", "archwright.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	state := realTemp(t)
	m := New(st, filepath.Join(state, "worktrees"), filepath.Join(state, "clones"))
	if _, err := m.Detect(remote.url); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Register(RegisterInput{URL: remote.url, BaseBranch: "main", Gates: &store.Gates{}}); err != nil {
		t.Fatal(err)
	}
	if site, err := m.Resolve(dev); err != nil || site.Name != "r" {
		t.Fatalf("%+v %v", site, err)
	}
	j, err := st.CreateJobOn("r", "T", "")
	if err != nil {
		t.Fatal(err)
	}
	integ, err := m.CreateWorktree(j.ID, store.KindIntegration, "")
	if err != nil {
		t.Fatal(err)
	}
	w, err := m.CreateWorktree(j.ID, store.KindWright, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateWorktree(j.ID, store.KindReadOnly, ""); err != nil {
		t.Fatal(err)
	}
	// Unpushed local commits and branches are invisible to the Job.
	for _, x := range []store.Worktree{integ, w} {
		if _, err := os.Stat(filepath.Join(x.Path, "local.txt")); err == nil {
			t.Errorf("%s worktree sees the developer's unpushed commit", x.Kind)
		}
		if _, err := os.Stat(filepath.Join(x.Path, "dirty.txt")); err == nil {
			t.Errorf("%s worktree sees the developer's uncommitted file", x.Kind)
		}
	}
	if _, err := m.CreateWorktree(j.ID, store.KindWright, "wip"); err == nil {
		t.Error("a local-only branch resolved as a base ref")
	}
	if _, err := m.CreateWorktree(j.ID, store.KindWright, run(t, dev, "rev-parse", "HEAD")); err == nil {
		t.Error("an unpushed commit resolved as a base ref")
	}
	// Work, clean up and close, accepting and discarding.
	os.WriteFile(filepath.Join(w.Path, "w.txt"), []byte("w"), 0o644)
	run(t, w.Path, "add", ".")
	run(t, w.Path, "commit", "-m", "work")
	run(t, integ.Path, "merge", "--no-ff", "-m", "merge", w.Branch)
	if _, err := m.CleanupMerged(j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Close(j.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
	j2, _ := st.CreateJobOn("r", "T2", "")
	m.CreateWorktree(j2.ID, store.KindIntegration, "")
	if _, err := m.Close(j2.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remove("r"); err != nil {
		t.Fatal(err)
	}

	if after := snapshot(); after != before {
		t.Errorf("the developer's checkout changed:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if got := run(t, dev, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("developer checkout moved to %s", got)
	}
}

func TestWorktreesLiveOutsideTheClone(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	mainBefore := run(t, e.clone(), "rev-parse", "refs/remotes/origin/main")

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
		if inside(w.Path, e.clone()) || inside(w.Path, e.dev) || !inside(w.Path, e.root) {
			t.Errorf("%s worktree at %s", w.Kind, w.Path)
		}
		if _, err := os.Stat(filepath.Join(w.Path, "a.txt")); err != nil {
			t.Errorf("%s worktree not checked out: %v", w.Kind, err)
		}
		if gitdir := run(t, w.Path, "rev-parse", "--git-common-dir"); !inside(gitdir, e.clone()) {
			t.Errorf("%s worktree's git dir is %s, not the Works' clone", w.Kind, gitdir)
		}
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
	// Commits in worktrees never move the remote base branch, and no local
	// branch named for it exists in the clone to be checked out.
	os.WriteFile(filepath.Join(wright.Path, "w.txt"), []byte("w"), 0o644)
	run(t, wright.Path, "add", ".")
	run(t, wright.Path, "commit", "-m", "work")
	if got := run(t, e.clone(), "rev-parse", "refs/remotes/origin/main"); got != mainBefore {
		t.Errorf("origin/main moved: %s -> %s", mainBefore, got)
	}
	if gitOK(e.clone(), "rev-parse", "--verify", "--quiet", "refs/heads/main") {
		t.Error("the clone grew a local main branch")
	}
}

func TestWorktreeStartsFromTheFreshlyFetchedRemoteBranch(t *testing.T) {
	e := setup(t)
	// Someone pushes after the Site was registered; the developer has not pulled.
	e.remote.commit("upstream", map[string]string{"new.txt": "n"})
	os.WriteFile(filepath.Join(e.dev, "local.txt"), []byte("l"), 0o644)
	run(t, e.dev, "add", ".")
	run(t, e.dev, "commit", "-m", "unpushed")

	w, err := e.m.CreateWorktree(e.job(t).ID, store.KindIntegration, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := run(t, e.remote.bare, "rev-parse", "main"); w.BaseSHA != want {
		t.Errorf("based on %s, remote main is %s", w.BaseSHA, want)
	}
	if _, err := os.Stat(filepath.Join(w.Path, "new.txt")); err != nil {
		t.Error("worktree lacks what the remote has")
	}
	if _, err := os.Stat(filepath.Join(w.Path, "local.txt")); err == nil {
		t.Error("worktree has an unpushed local commit")
	}
}

func TestWorktreeFailsClosedWhenTheRemoteCannotBeFetched(t *testing.T) {
	e := setup(t)
	if err := os.RemoveAll(e.remote.bare); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CreateWorktree(e.job(t).ID, store.KindIntegration, ""); err == nil {
		t.Fatal("worktree created from a stale clone")
	}
}

func TestWorktreeBaseRefAndGuards(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	e.remote.branch("feature")
	w, err := e.m.CreateWorktree(j.ID, store.KindWright, "feature")
	if err != nil || w.BaseRef != "feature" || w.BaseSHA != run(t, e.remote.bare, "rev-parse", "feature") {
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

func TestRemoveDeletesTheCloneAndKeepsJobHistory(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	w, err := e.m.CreateWorktree(j.ID, store.KindWright, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Remove("demo"); err == nil {
		t.Fatal("removed a Site whose Job still has worktrees")
	}
	if _, err := os.Stat(e.clone()); err != nil {
		t.Fatal("clone deleted by a refused removal")
	}
	if _, err := e.m.Close(j.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Remove("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.clone()); !os.IsNotExist(err) {
		t.Error("clone kept after removal")
	}
	if _, err := os.Stat(e.dev); err != nil {
		t.Error("developer's checkout disturbed")
	}
	if got, err := e.st.GetJob(j.ID); err != nil || got.Site != "demo" || got.Outcome != "discarded" {
		t.Fatalf("job history: %+v %v", got, err)
	}
	if wts, err := e.st.Worktrees(j.ID); err != nil || len(wts) != 1 || wts[0].ID != w.ID {
		t.Fatalf("worktree history: %+v %v", wts, err)
	}
	if _, err := e.m.Resolve(e.dev); err == nil {
		t.Error("removed Site still resolves")
	}
	if _, err := e.m.Remove("demo"); err == nil {
		t.Error("removed twice")
	}
	// It can be registered again.
	if _, err := e.m.Register(RegisterInput{Name: "demo", URL: e.remote.url, Gates: &store.Gates{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Resolve(e.dev); err != nil {
		t.Error(err)
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
	// Merge Wright branches into the integration branch.
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
	if gitOK(e.clone(), "rev-parse", "--verify", "--quiet", "refs/heads/"+merged.Branch) {
		t.Error("merged branch kept")
	}
	if !gitOK(e.clone(), "rev-parse", "--verify", "--quiet", "refs/heads/"+unmerged.Branch) {
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
			if got := gitOK(e.clone(), "rev-parse", "--verify", "--quiet", "refs/heads/"+integ.Branch); got != wantBranch {
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

func TestFreshWrightIsNotMerged(t *testing.T) {
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

func TestConcurrentRegistrationsOfOneRepoLeaveOneWorkingSite(t *testing.T) {
	e := setup(t)
	r := e.hosts.create(t, "o/race", map[string]string{"a": "a"})
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, err := e.m.Register(RegisterInput{Name: "race", URL: r.url, BaseBranch: "main", Gates: &store.Gates{}})
			errs <- err
		}()
	}
	won := 0
	for i := 0; i < 4; i++ {
		if err := <-errs; err == nil {
			won++
		} else if !errors.Is(err, store.ErrExists) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d registrations succeeded", won)
	}
	site, err := e.st.GetSite("race")
	if err != nil || run(t, site.ClonePath, "rev-parse", "refs/remotes/origin/main") == "" {
		t.Fatalf("the registered Site has no working clone: %+v %v", site, err)
	}
}

func TestProbeDirIsTheMainCheckoutEveryJobWorktreeIsMadeFrom(t *testing.T) {
	e := setup(t)
	dir, err := e.m.ProbeDir("demo")
	if err != nil {
		t.Fatal(err)
	}
	if dir != e.clone() || dir != filepath.Join(e.clones, "demo", "main") {
		t.Errorf("probe dir %s, want the main checkout %s", dir, e.clone())
	}
	w, err := e.m.CreateWorktree(e.job(t).ID, store.KindIntegration, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, w.Path, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != filepath.Join(dir, ".git") {
		t.Errorf("a Job worktree is made from %s, not the main checkout %s", got, dir)
	}
	if _, err := e.m.ProbeDir("nope"); err == nil {
		t.Error("a probe dir of a Site that is not registered")
	}
}

func TestMainCheckoutFollowsTheFetchedBaseBranchAndStaysDetached(t *testing.T) {
	e := setup(t)
	e.remote.commit("second", map[string]string{"b.txt": "b"})
	if _, err := e.m.CreateWorktree(e.job(t).ID, store.KindIntegration, ""); err != nil {
		t.Fatal(err)
	}
	if got, want := run(t, e.clone(), "rev-parse", "HEAD"), run(t, e.remote.bare, "rev-parse", "main"); got != want {
		t.Errorf("main checkout at %s, want the fetched base %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(e.clone(), "b.txt")); err != nil {
		t.Errorf("main checkout files not updated: %v", err)
	}
	if exec.Command("git", "-C", e.clone(), "symbolic-ref", "-q", "HEAD").Run() == nil {
		t.Error("main checkout is on a branch")
	}
}

// legacySite registers a Site the way the Works did before ADR 0030: a bare
// clone at <clones>/<name>.git.
func (e env) legacySite(t *testing.T, name string, rp repo) store.Site {
	t.Helper()
	clone := legacyClone(e.clones, name)
	if err := fetchBare(clone, rp.url, nil); err != nil {
		t.Fatal(err)
	}
	s, err := e.st.AddSite(store.Site{Name: name, URL: rp.url, ClonePath: clone, BaseBranch: "main", ConcurrencyCap: 3, Gates: store.Gates{Test: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrateReplacesABareCloneKeepingJobHistory(t *testing.T) {
	e := setup(t)
	rp := e.hosts.create(t, "o/old", map[string]string{"old.txt": "old"})
	old := e.legacySite(t, "old", rp)
	j, err := e.st.CreateJobOn("old", "finished", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CloseJob(j.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	if need, err := e.m.NeedsMigration("old"); err != nil || !need {
		t.Fatalf("a bare clone does not need migration: %v %v", need, err)
	}
	if need, _ := e.m.NeedsMigration("demo"); need {
		t.Error("a normal clone needs migration")
	}
	// Until migrated the probe runs where the worktrees sit, as before.
	if dir, err := e.m.ProbeDir("old"); err != nil || dir != filepath.Join(e.root, "old") {
		t.Errorf("probe dir of an unmigrated Site: %s %v", dir, err)
	}

	migrated, err := e.m.Migrate("old")
	if err != nil || !migrated {
		t.Fatalf("migrate: %v %v", migrated, err)
	}
	s, err := e.st.GetSite("old")
	if err != nil {
		t.Fatal(err)
	}
	if want := mainDir(e.clones, "old"); s.ClonePath != want {
		t.Errorf("clone path %s, want %s", s.ClonePath, want)
	}
	if got := run(t, s.ClonePath, "rev-parse", "--is-bare-repository"); got != "false" {
		t.Errorf("migrated clone is bare: %s", got)
	}
	if _, err := os.Stat(filepath.Join(s.ClonePath, "old.txt")); err != nil {
		t.Errorf("main checkout has no files: %v", err)
	}
	if _, err := os.Stat(old.ClonePath); !os.IsNotExist(err) {
		t.Error("the old bare clone was kept")
	}
	if got, err := e.st.GetJob(j.ID); err != nil || got.Site != "old" || got.Outcome != "accepted" {
		t.Errorf("job history: %+v %v", got, err)
	}
	if dir, err := e.m.ProbeDir("old"); err != nil || dir != s.ClonePath {
		t.Errorf("probe dir after migration: %s %v", dir, err)
	}
	// Job worktrees are created from the new clone, and a second migration does nothing.
	nj, err := e.st.CreateJobOn("old", "new", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CreateWorktree(nj.ID, store.KindIntegration, ""); err != nil {
		t.Fatal(err)
	}
	if migrated, err := e.m.Migrate("old"); err != nil || migrated {
		t.Errorf("second migration: %v %v", migrated, err)
	}
	if migrated, err := e.m.Migrate("demo"); err != nil || migrated {
		t.Errorf("migrating a normal clone: %v %v", migrated, err)
	}
}

func TestMigrateWaitsForActiveJobs(t *testing.T) {
	e := setup(t)
	rp := e.hosts.create(t, "o/old", map[string]string{"old.txt": "old"})
	old := e.legacySite(t, "old", rp)
	j, err := e.st.CreateJobOn("old", "running", "")
	if err != nil {
		t.Fatal(err)
	}
	w, err := e.m.CreateWorktree(j.ID, store.KindIntegration, "")
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := e.m.Migrate("old")
	var blocked *BlockedError
	if migrated || !errors.As(err, &blocked) || !IsBlocked(err) || len(blocked.Jobs) != 1 || blocked.Jobs[0] != j.ID {
		t.Fatalf("migrate with an active Job: %v %v", migrated, err)
	}
	if s, _ := e.st.GetSite("old"); s.ClonePath != old.ClonePath {
		t.Errorf("clone path moved to %s", s.ClonePath)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Errorf("the Job's worktree is gone: %v", err)
	}
	if _, err := os.Stat(mainDir(e.clones, "old")); !os.IsNotExist(err) {
		t.Error("a main checkout was made while blocked")
	}
	// Once the Job closes it migrates.
	if _, err := e.m.Close(j.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
	if migrated, err := e.m.Migrate("old"); err != nil || !migrated {
		t.Fatalf("migrate after close: %v %v", migrated, err)
	}
}

func TestMigrateFailureLeavesTheBareCloneInPlace(t *testing.T) {
	e := setup(t)
	rp := e.hosts.create(t, "o/old", map[string]string{"old.txt": "old"})
	old := e.legacySite(t, "old", rp)
	if err := os.RemoveAll(rp.bare); err != nil { // the remote is gone
		t.Fatal(err)
	}
	if migrated, err := e.m.Migrate("old"); err == nil || migrated {
		t.Fatalf("migrate with an unreachable remote: %v %v", migrated, err)
	}
	if s, _ := e.st.GetSite("old"); s.ClonePath != old.ClonePath {
		t.Errorf("clone path moved to %s", s.ClonePath)
	}
	if !gitOK(old.ClonePath, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main") {
		t.Error("the bare clone was damaged")
	}
	if _, err := os.Stat(filepath.Dir(mainDir(e.clones, "old"))); !os.IsNotExist(err) {
		t.Error("a half-made main checkout was left")
	}
}

// deliverSkills puts the Guild's skills into a worktree as a run would.
func deliverSkills(t *testing.T, w store.Worktree, names ...string) {
	t.Helper()
	var set []skills.Skill
	for _, n := range names {
		set = append(set, skills.Skill{Name: n, FS: fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: " + n + "\n---\n")}}})
	}
	if _, err := skills.Deliver(skills.Request{Worktree: w.Path, RunDir: filepath.Join(realTemp(t), "run-"+w.ID), Runner: "claude", Skills: set}); err != nil {
		t.Fatal(err)
	}
}

func TestCloseAcceptRemovesWorktreeHoldingOnlyDeliveredSkills(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	integ, _ := e.m.CreateWorktree(j.ID, store.KindIntegration, "")
	w, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
	deliverSkills(t, w, "to-spec", "wayfinder")
	deliverSkills(t, integ, "to-spec")
	if _, err := e.m.Close(j.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	for _, x := range []store.Worktree{integ, w} {
		if _, err := os.Stat(x.Path); !os.IsNotExist(err) {
			t.Errorf("%s kept", x.Kind)
		}
	}
	if job, _ := e.st.GetJob(j.ID); job.Outcome != "accepted" {
		t.Errorf("outcome %q", job.Outcome)
	}
}

func TestCloseAcceptWithDeliveredSkillsStillRefusesRealChanges(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	w, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
	deliverSkills(t, w, "to-spec")
	os.WriteFile(filepath.Join(w.Path, "dirty.txt"), []byte("x"), 0o644)
	if _, err := e.m.Close(j.ID, "accepted"); err == nil {
		t.Fatal("accepted with uncommitted work")
	}
	if _, err := os.Stat(filepath.Join(w.Path, "dirty.txt")); err != nil {
		t.Error("real work removed")
	}
	if job, _ := e.st.GetJob(j.ID); job.Outcome != "" {
		t.Errorf("job closed: %q", job.Outcome)
	}
}

func TestCloseAcceptRemovesOnlyTheWorksOwnSkillEntries(t *testing.T) {
	e := setup(t)
	e.hosts.create(t, "o/own", map[string]string{
		".agents/skills/mine/SKILL.md": "mine",
		".claude/skills/mine/SKILL.md": "mine",
	})
	if _, err := e.m.Register(RegisterInput{Name: "own", URL: host + "o/own.git", BaseBranch: "main", Gates: &store.Gates{}}); err != nil {
		t.Fatal(err)
	}
	j, err := e.st.CreateJobOn("own", "a job", "")
	if err != nil {
		t.Fatal(err)
	}
	w, err := e.m.CreateWorktree(j.ID, store.KindWright, "")
	if err != nil {
		t.Fatal(err)
	}
	deliverSkills(t, w, "to-spec")
	if _, err := os.Stat(filepath.Join(w.Path, ".agents/skills/to-spec/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	// the removal clears only the delivered entries, so tracked files survive
	if err := skills.RemoveDelivered(w.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Path, ".agents/skills/mine/SKILL.md")); err != nil {
		t.Errorf("tracked skill removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(w.Path, ".claude/skills/mine/SKILL.md")); err != nil {
		t.Errorf("tracked link removed: %v", err)
	}
	for _, gone := range []string{".agents/skills/to-spec", ".claude/skills/to-spec"} {
		if _, err := os.Lstat(filepath.Join(w.Path, gone)); !os.IsNotExist(err) {
			t.Errorf("%s kept", gone)
		}
	}
	deliverSkills(t, w, "to-spec")
	if _, err := e.m.Close(j.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupMergedRemovesMergedWorktreeHoldingOnlyDeliveredSkills(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	integ, _ := e.m.CreateWorktree(j.ID, store.KindIntegration, "")
	w, _ := e.m.CreateWorktree(j.ID, store.KindWright, "")
	os.WriteFile(filepath.Join(w.Path, "b.txt"), []byte("b"), 0o644)
	run(t, w.Path, "add", "b.txt")
	run(t, w.Path, "-c", "user.name=t", "-c", "user.email=t@e.com", "commit", "-q", "-m", "b")
	run(t, integ.Path, "merge", "--ff-only", w.Branch)
	deliverSkills(t, w, "to-spec")
	removed, err := e.m.CleanupMerged(j.ID)
	if err != nil || len(removed) != 1 {
		t.Fatalf("%v %v", removed, err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Error("merged worktree kept")
	}
}

func TestWardenWorktreeKinds(t *testing.T) {
	e := setup(t)
	job := e.job(t).ID

	empty, err := e.m.WardenWorktree(job, defs.WorktreeEmpty)
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(empty.Path); len(entries) != 1 || entries[0].Name() != ".git" {
		t.Errorf("empty worktree holds %v", entries)
	}
	if out := run(t, empty.Path, "status", "--porcelain"); out != "" {
		t.Errorf("empty worktree's git status is not quiet: %q", out)
	}
	if empty.Kind != store.KindEmpty {
		t.Errorf("kind %s", empty.Kind)
	}

	detached, err := e.m.WardenWorktree(job, defs.WorktreeDetached)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(detached.Path, "a.txt")); err != nil {
		t.Error("detached worktree is not a full checkout")
	}
	if b := run(t, detached.Path, "branch", "--show-current"); b != "" {
		t.Errorf("detached worktree is on branch %q", b)
	}

	first, err := e.m.WardenWorktree(job, defs.WorktreeIntegration)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.m.WardenWorktree(job, defs.WorktreeIntegration)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != again.ID || first.Path != again.Path {
		t.Errorf("integration worktree not shared: %s and %s", first.Path, again.Path)
	}
	if _, err := os.Stat(filepath.Join(first.Path, "a.txt")); err != nil {
		t.Error("integration worktree is not a full checkout")
	}

	branch, err := e.m.WardenWorktree(job, defs.WorktreeBranch)
	if err != nil {
		t.Fatal(err)
	}
	if b := run(t, branch.Path, "branch", "--show-current"); b == "" || b != branch.Branch || b == first.Branch {
		t.Errorf("branch worktree is on %q, record says %q", b, branch.Branch)
	}
	if branch.Kind != store.KindWarden {
		t.Errorf("branch worktree kind is %s", branch.Kind)
	}
	if _, err := e.m.WardenWorktree(job, "weird"); err == nil {
		t.Error("an unknown kind was accepted")
	}
}

func TestEmptyWorktreeRemovesWithItsJob(t *testing.T) {
	e := setup(t)
	job := e.job(t).ID
	empty, err := e.m.WardenWorktree(job, defs.WorktreeEmpty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Close(job, "accepted"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(empty.Path); err == nil {
		t.Error("empty worktree outlived its Job")
	}
}
