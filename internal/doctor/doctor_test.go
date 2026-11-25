package doctor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/store"
)

// newDeps is a machine where everything passes: both runners installed and
// answering, a private config and state directory, no Works running.
func newDeps(t *testing.T) Deps {
	t.Helper()
	root := t.TempDir()
	dirs := paths.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	found := map[string]string{"archwright": "/usr/bin/archwright", "claude": "/bin/claude", "codex": "/bin/codex", "lavish-axi": "/bin/lavish-axi", "gh": "/bin/gh", "tmux": "/bin/tmux"}
	return Deps{
		Dirs:     dirs,
		Getenv:   func(string) string { return "" },
		LookPath: func(n string) (string, error) { return lookup(found, n) },
		Output: func(_ context.Context, name string, args ...string) (string, error) {
			if filepath.Base(name) == "tmux" {
				return "tmux 3.8", nil
			}
			if len(args) > 0 && args[0] == "--version" {
				return "1.2.3 (" + filepath.Base(name) + ")", nil
			}
			if filepath.Base(name) == "gh" {
				return "Logged in to github.com", nil
			}
			return "", nil
		},
		Hooks:        fakeHooks{Socket: true, Spool: true},
		PolicyFiles:  func(string) []string { return nil },
		WorksRunning: func() (int, int, bool) { return 0, 0, false },
		TempDir:      "/tmp",
	}
}

func lookup(found map[string]string, n string) (string, error) {
	if p, ok := found[n]; ok {
		return p, nil
	}
	return "", exec.ErrNotFound
}

func byName(rep Report, name string) Check {
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{Name: name, Status: "absent"}
}

func TestEverythingPassing(t *testing.T) {
	rep := Run(context.Background(), newDeps(t))
	if !rep.OK {
		t.Fatalf("failures: %+v", rep.Failures())
	}
	for _, name := range []string{"runner:claude", "runner:codex", "directory:config", "schema", "works", "skills", "lavish-axi"} {
		if c := byName(rep, name); c.Status != Pass {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

func TestMissingLavishWarnsWithFixAndDoesNotBlock(t *testing.T) {
	d := newDeps(t)
	d.LookPath = func(n string) (string, error) {
		if n == "lavish-axi" {
			return "", exec.ErrNotFound
		}
		return "/bin/" + n, nil
	}
	rep := Run(context.Background(), d)
	c := byName(rep, "lavish-axi")
	if c.Status != Warn || !strings.Contains(c.Fix, "npm install -g lavish-axi") {
		t.Fatalf("lavish: %+v", c)
	}
	if !rep.OK {
		t.Fatalf("a missing lavish-axi blocked runs: %+v", rep.Failures())
	}
	if err := NewGate(func(context.Context) Report { return rep }).Check(context.Background()); err != nil {
		t.Fatalf("gate refused over a warning: %v", err)
	}
}

func TestRunnerPresence(t *testing.T) {
	d := newDeps(t)
	d.LookPath = func(n string) (string, error) {
		if n == "claude" || n == "codex" {
			return "", exec.ErrNotFound
		}
		return "/bin/" + n, nil
	}
	rep := Run(context.Background(), d) // the default choice is claude
	if c := byName(rep, "runner:claude"); c.Status != Fail || !strings.Contains(c.Fix, "install Claude Code") {
		t.Errorf("chosen runner: %+v", c)
	}
	if c := byName(rep, "runner:codex"); c.Status != Warn {
		t.Errorf("other runner: %+v", c)
	}
}

func TestDirectoryPermissions(t *testing.T) {
	d := newDeps(t)
	if err := os.Chmod(d.Dirs.State, 0o755); err != nil {
		t.Fatal(err)
	}
	rep := Run(context.Background(), d)
	c := byName(rep, "directory:state")
	if c.Status != Fail || c.Fix != "chmod 700 "+d.Dirs.State {
		t.Fatalf("%+v", c)
	}
}

func TestBadConfigFails(t *testing.T) {
	d := newDeps(t)
	if err := os.WriteFile(filepath.Join(d.Dirs.Config, "config.toml"), []byte(`runner = "gemini"`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := byName(Run(context.Background(), d), "config")
	if c.Status != Fail || !strings.Contains(c.Fix, "config.toml") {
		t.Fatalf("%+v", c)
	}
}

func TestMissingArchwrightOnPathFails(t *testing.T) {
	d := newDeps(t)
	look := d.LookPath
	d.LookPath = func(n string) (string, error) {
		if n == "archwright" {
			return "", exec.ErrNotFound
		}
		return look(n)
	}
	if c := byName(Run(context.Background(), d), "archwright-on-path"); c.Status != Fail || c.Fix == "" {
		t.Fatalf("%+v", c)
	}
}

func TestSchemaNewerThanBinaryFails(t *testing.T) {
	d := newDeps(t)
	st, err := store.Open(d.Dirs.Database())
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	db, err := sql.Open("sqlite", d.Dirs.Database())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	c := byName(Run(context.Background(), d), "schema")
	if c.Status != Fail || !strings.Contains(c.Fix, "rebuild") {
		t.Fatalf("%+v", c)
	}
}

func TestWorksReachability(t *testing.T) {
	d := newDeps(t)
	d.WorksRunning = func() (int, int, bool) { return 42, 5555, true }
	if c := byName(Run(context.Background(), d), "works"); c.Status != Pass || !strings.Contains(c.Detail, "5555") {
		t.Fatalf("running: %+v", c)
	}
	d.WorksRunning = func() (int, int, bool) { return 0, 0, false }
	if err := os.WriteFile(d.Dirs.PidFile(), []byte(`{"pid":1,"port":2,"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := byName(Run(context.Background(), d), "works"); c.Status != Warn || c.Fix == "" {
		t.Fatalf("stale: %+v", c)
	}
}

func TestGateRefusesWithTheFixesAndRemembersBriefly(t *testing.T) {
	now := time.Unix(1000, 0)
	calls := 0
	var rep Report
	g := NewGate(func(context.Context) Report { calls++; return rep })
	g.Now = func() time.Time { return now }

	rep = Report{Checks: []Check{{Name: "runner:claude", Status: Fail, Detail: "not installed", Fix: "install it"}}}
	err := g.Check(context.Background())
	var fe *FailedError
	if !errors.As(err, &fe) || !strings.Contains(err.Error(), "install it") || !strings.Contains(err.Error(), "runs do not start") {
		t.Fatalf("got %v", err)
	}
	g.Check(context.Background())
	if calls != 1 {
		t.Fatalf("failure not remembered: %d runs", calls)
	}
	rep = Report{OK: true}
	now = now.Add(time.Minute) // past the failure's memory: the developer fixed it
	if err := g.Check(context.Background()); err != nil {
		t.Fatalf("still refusing after the fix: %v", err)
	}
	g.Check(context.Background())
	if calls != 2 {
		t.Fatalf("pass not remembered: %d runs", calls)
	}
}
