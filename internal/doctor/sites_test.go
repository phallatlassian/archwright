package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

func git(t *testing.T, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// siteDeps registers one Site "demo" whose remote is a local repository (spelled
// as an https URL through git's insteadOf) and whose clone is the Works' normal clone of it (ADR 0030).
func siteDeps(t *testing.T, pocock bool, gates store.Gates) (Deps, string) {
	t.Helper()
	d := newDeps(t)
	d.Output = func(ctx context.Context, name string, args ...string) (string, error) {
		if filepath.Base(name) == "gh" { // never the developer's real gh
			return "Logged in to github.com", nil
		}
		return execOutput(ctx, name, args...)
	}
	host, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+host+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://git.example.com/")
	work := filepath.Join(host, "o", "r")
	git(t, "init", "-b", "main", work)
	if pocock {
		os.MkdirAll(filepath.Join(work, "docs", "agents"), 0o755)
		os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("## Agent skills\n"), 0o644)
		for _, f := range pocockDocs {
			os.WriteFile(filepath.Join(work, f), []byte("x"), 0o644)
		}
	} else {
		os.WriteFile(filepath.Join(work, "README.md"), []byte("x"), 0o644)
	}
	git(t, "-C", work, "add", "-A")
	git(t, "-C", work, "commit", "-m", "init")
	clone := filepath.Join(d.Dirs.State, "clones", "demo", "main")
	os.MkdirAll(filepath.Dir(clone), 0o700)
	git(t, "clone", "--quiet", "--no-checkout", "https://git.example.com/o/r", clone)
	git(t, "-C", clone, "checkout", "--quiet", "--detach", "refs/remotes/origin/main")

	st, err := store.Open(d.Dirs.Database())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSite(store.Site{Name: "demo", URL: "https://git.example.com/o/r", ClonePath: clone, BaseBranch: "main", ConcurrencyCap: 3, Gates: gates}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	return d, work
}

func TestSiteHealthy(t *testing.T) {
	d, _ := siteDeps(t, true, store.Gates{Test: "go test ./..."})
	d.LookPath = func(n string) (string, error) { return "/bin/" + n, nil }
	rep := Run(context.Background(), d)
	for _, name := range []string{"site-repo:demo", "site-skills:demo", "site-gates:demo"} {
		if c := byName(rep, name); c.Status != Pass {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

func TestSiteWithoutGatesFailsWithTheCommandToPropose(t *testing.T) {
	d, _ := siteDeps(t, true, store.Gates{})
	rep := Run(context.Background(), d)
	c := byName(rep, "site-gates:demo")
	if c.Status != Fail || !strings.Contains(c.Fix, "archwright site gates demo") || !strings.Contains(c.Fix, "gate-approve") {
		t.Fatalf("%+v", c)
	}
	if rep.OK {
		t.Fatal("a Site with no gates passed")
	}
}

func TestSiteWithoutSkillsConfigurationWarns(t *testing.T) {
	d, _ := siteDeps(t, false, store.Gates{Test: "true"})
	c := byName(Run(context.Background(), d), "site-skills:demo")
	if c.Status != Warn || !strings.Contains(c.Detail, "docs/agents/issue-tracker.md") || !strings.Contains(c.Fix, "setup-matt-pocock-skills") {
		t.Fatalf("%+v", c)
	}
}

func TestSiteGateToolMissingWarnsAndPendingChangeIsReported(t *testing.T) {
	d, _ := siteDeps(t, true, store.Gates{Test: "nosuchtool run"})
	st, err := store.Open(d.Dirs.Database())
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.ProposeGates("demo", store.Gates{Test: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	rep := Run(context.Background(), d)
	var warns []Check
	for _, c := range rep.Checks {
		if c.Name == "site-gates:demo" {
			warns = append(warns, c)
		}
	}
	if len(warns) != 2 {
		t.Fatalf("%+v", warns)
	}
	if !strings.Contains(warns[0].Fix, "gate-approve "+ch.ID) {
		t.Errorf("pending: %+v", warns[0])
	}
	if warns[1].Status != Warn || !strings.Contains(warns[1].Detail, "nosuchtool") {
		t.Errorf("tool: %+v", warns[1])
	}
}

func TestSiteCloneProblems(t *testing.T) {
	d, _ := siteDeps(t, true, store.Gates{Test: "true"})
	clone := filepath.Join(d.Dirs.State, "clones", "demo", "main")
	git(t, "-C", clone, "update-ref", "-d", "refs/remotes/origin/main")
	c := byName(Run(context.Background(), d), "site-repo:demo")
	if c.Status != Fail || !strings.Contains(c.Fix, "fetch origin") {
		t.Fatalf("no base branch: %+v", c)
	}
	os.RemoveAll(clone)
	c = byName(Run(context.Background(), d), "site-repo:demo")
	if c.Status != Fail || !strings.Contains(c.Fix, "archwright site register") {
		t.Fatalf("no clone: %+v", c)
	}
}

// legacyClone swaps the Site's normal clone for the bare clone it had before ADR 0030.
func legacyClone(t *testing.T, d Deps) string {
	t.Helper()
	bare := filepath.Join(d.Dirs.State, "clones", "demo.git")
	os.MkdirAll(bare, 0o700)
	git(t, "init", "--bare", "--quiet", bare)
	git(t, "-C", bare, "remote", "add", "origin", "https://git.example.com/o/r")
	git(t, "-C", bare, "fetch", "--quiet", "origin")
	st, err := store.Open(d.Dirs.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetClonePath("demo", bare); err != nil {
		t.Fatal(err)
	}
	return bare
}

func TestSiteNormalCloneIsReportedAsSuch(t *testing.T) {
	d, _ := siteDeps(t, true, store.Gates{Test: "true"})
	if c := byName(Run(context.Background(), d), "site-clone:demo"); c.Status != Pass || !strings.Contains(c.Detail, "main checkout") {
		t.Fatalf("%+v", c)
	}
}

func TestSiteBareCloneWarnsThatItsMigrationIsWaiting(t *testing.T) {
	d, _ := siteDeps(t, true, store.Gates{Test: "true"})
	legacyClone(t, d)
	// Not enabled: the warning says how to enable it.
	d.Getenv = func(string) string { return "" }
	c := byName(Run(context.Background(), d), "site-clone:demo")
	if c.Status != Warn || !strings.Contains(c.Fix, "ARCHWRIGHT_MIGRATE_CLONES") {
		t.Fatalf("not enabled: %+v", c)
	}
	if r := byName(Run(context.Background(), d), "site-repo:demo"); r.Status != Pass {
		t.Errorf("a bare clone is still a working clone: %+v", r)
	}
	// Enabled: it moves at the next start.
	d.Getenv = func(k string) string {
		if k == "ARCHWRIGHT_MIGRATE_CLONES" {
			return "1"
		}
		return ""
	}
	if c := byName(Run(context.Background(), d), "site-clone:demo"); c.Status != Warn || !strings.Contains(c.Detail, "next Works start") {
		t.Fatalf("enabled: %+v", c)
	}
	// An active Job holds it back.
	st, err := store.Open(d.Dirs.Database())
	if err != nil {
		t.Fatal(err)
	}
	j, err := st.CreateJobOn("demo", "running", "")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if c := byName(Run(context.Background(), d), "site-clone:demo"); c.Status != Warn || !strings.Contains(c.Detail, "waiting for active Jobs") || !strings.Contains(c.Detail, j.ID) {
		t.Fatalf("active Job: %+v", c)
	}
}

func TestSiteRemoteUnreachableWarns(t *testing.T) {
	d, work := siteDeps(t, true, store.Gates{Test: "true"})
	os.RemoveAll(work)
	c := byName(Run(context.Background(), d), "site-repo:demo")
	if c.Status != Warn || !strings.Contains(c.Fix, "git ls-remote") {
		t.Fatalf("%+v", c)
	}
}

// Doctor changes nothing: a full run over a populated config and state
// directory leaves every file as it was, byte for byte and in mode.
func TestDoctorChangesNothing(t *testing.T) {
	d, _ := siteDeps(t, true, store.Gates{Test: "go test ./..."})
	os.WriteFile(filepath.Join(d.Dirs.Config, "config.toml"), []byte(`runner = "claude"`), 0o600)
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, root := range []string{d.Dirs.Config, d.Dirs.State} {
			filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				content := ""
				if fi.Mode().IsRegular() {
					b, _ := os.ReadFile(p)
					content = string(b)
				}
				out[p] = fi.Mode().String() + "|" + fi.ModTime().String() + "|" + content
				return nil
			})
		}
		return out
	}
	before := snapshot()
	Run(context.Background(), d)
	after := snapshot()
	if len(before) != len(after) {
		t.Fatalf("files appeared or vanished: %d -> %d", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed", p)
		}
	}
}
