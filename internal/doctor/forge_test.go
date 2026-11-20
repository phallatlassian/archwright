package doctor

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

// withGH makes gh absent (missing), or answering `auth status` with out and err.
func withGH(d Deps, missing bool, out string, err error) (Deps, *[]string) {
	var calls []string
	look := d.LookPath
	d.LookPath = func(n string) (string, error) {
		if n == "gh" && missing {
			return "", exec.ErrNotFound
		}
		return look(n)
	}
	prev := d.Output
	d.Output = func(ctx context.Context, name string, args ...string) (string, error) {
		if strings.HasSuffix(name, "gh") {
			calls = append(calls, strings.Join(args, " "))
			return out, err
		}
		return prev(ctx, name, args...)
	}
	return d, &calls
}

func addSite(t *testing.T, d Deps, name, url string) {
	t.Helper()
	st, err := store.Open(d.Dirs.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddSite(store.Site{Name: name, URL: url, ClonePath: d.Dirs.State + "/none", BaseBranch: "main", ConcurrencyCap: 3, Gates: store.Gates{Test: "true"}}); err != nil {
		t.Fatal(err)
	}
}

func TestGHSignedInPasses(t *testing.T) {
	d, calls := withGH(newDeps(t), false, "Logged in to github.com", nil)
	rep := Run(context.Background(), d)
	if c := byName(rep, "forge:gh"); c.Status != Pass {
		t.Errorf("%+v", c)
	}
	if c := byName(rep, "forge:gh-auth"); c.Status != Pass {
		t.Errorf("%+v", c)
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "auth status") {
		t.Fatalf("doctor ran something besides a read of gh's sign-in: %q", *calls)
	}
}

func TestMissingGHBlocksRunsWhenASiteIsOnGitHub(t *testing.T) {
	d, _ := withGH(newDeps(t), true, "", nil)
	addSite(t, d, "app", "https://github.com/acme/app")
	rep := Run(context.Background(), d)
	c := byName(rep, "forge:gh")
	if c.Status != Fail || !strings.Contains(c.Fix, "brew install gh") || !strings.Contains(c.Fix, "gh auth login") || !strings.Contains(c.Detail, "app") {
		t.Fatalf("%+v", c)
	}
	if err := NewGate(func(context.Context) Report { return rep }).Check(context.Background()); err == nil || !strings.Contains(err.Error(), "brew install gh") {
		t.Fatalf("the gate let a GitHub Site run without gh: %v", err)
	}
}

func TestUnauthenticatedGHBlocksRunsWhenASiteIsOnGitHub(t *testing.T) {
	d, _ := withGH(newDeps(t), false, "You are not logged into any GitHub hosts. To log in, run: gh auth login", errors.New("exit status 1"))
	addSite(t, d, "app", "git@github.com:acme/app.git")
	rep := Run(context.Background(), d)
	c := byName(rep, "forge:gh-auth")
	if c.Status != Fail || !strings.Contains(c.Fix, "gh auth login") || !strings.Contains(c.Fix, "never signs in") || !strings.Contains(c.Detail, "not signed in") {
		t.Fatalf("%+v", c)
	}
	if rep.OK {
		t.Fatal("a GitHub Site with gh signed out passed")
	}
}

func TestGHProblemsOnlyWarnWithoutAGitHubSite(t *testing.T) {
	for name, tc := range map[string]struct {
		missing bool
		out     string
		err     error
		check   string
	}{
		"missing":         {missing: true, check: "forge:gh"},
		"unauthenticated": {out: "You are not logged into any GitHub hosts. To log in, run: gh auth login", err: errors.New("exit 1"), check: "forge:gh-auth"},
	} {
		t.Run(name, func(t *testing.T) {
			d, _ := withGH(newDeps(t), tc.missing, tc.out, tc.err)
			addSite(t, d, "local", "https://git.example.com/o/r") // no adapter: local-only (ADR 0022)
			rep := Run(context.Background(), d)
			c := byName(rep, tc.check)
			if c.Status != Warn || c.Fix == "" {
				t.Fatalf("%+v", c)
			}
			for _, f := range rep.Failures() {
				if strings.HasPrefix(f.Name, "forge:") {
					t.Fatalf("a forge check blocked a Site that needs no Forge: %+v", f)
				}
			}
		})
	}
}

func TestGHNetworkFaultWarnsEvenForAGitHubSite(t *testing.T) {
	d, _ := withGH(newDeps(t), false, "error connecting to api.github.com: dial tcp: no such host", errors.New("exit 1"))
	addSite(t, d, "app", "https://github.com/acme/app")
	c := byName(Run(context.Background(), d), "forge:gh-auth")
	if c.Status != Warn || !strings.Contains(c.Detail, "could not verify") {
		t.Fatalf("%+v", c)
	}
}
