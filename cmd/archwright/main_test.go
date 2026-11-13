package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "archwright-bin")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "archwright")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type sandbox struct {
	t   *testing.T
	env []string
	cfg string
	st  string
}

// newSandbox points the real binary at private directories and stops its Works on cleanup.
func newSandbox(t *testing.T) *sandbox {
	root := t.TempDir()
	s := &sandbox{t: t, cfg: filepath.Join(root, "cfg"), st: filepath.Join(root, "state")}
	s.env = append(os.Environ(), "ARCHWRIGHT_CONFIG_HOME="+s.cfg, "ARCHWRIGHT_STATE_HOME="+s.st)
	t.Cleanup(func() { s.run("stop") })
	return s
}

func (s *sandbox) cmd(stdin string, args ...string) (string, error) {
	c := exec.Command(binary, args...)
	c.Env = s.env
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	return string(out), err
}

func (s *sandbox) run(args ...string) string {
	s.t.Helper()
	out, err := s.cmd("", args...)
	if err != nil {
		s.t.Fatalf("archwright %v: %v\n%s", args, err, out)
	}
	return out
}

func TestCreateJobAndPlanReadBackThroughCLI(t *testing.T) {
	s := newSandbox(t)
	var job struct{ ID, Title string }
	if err := json.Unmarshal([]byte(s.run("job", "create", "--title", "Fix login", "--description", "broken")), &job); err != nil {
		t.Fatal(err)
	}
	plan := `{"steps":[{"id":"a","guild":"build","title":"fix"},{"id":"b","guild":"review","title":"check","blocked_by":["a"],"checkpoint":true}]}`
	if out, err := s.cmd(plan, "plan", "set", job.ID); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	var show struct {
		Title string
		Plan  struct{ Steps []struct{ ID, Guild string } }
	}
	if err := json.Unmarshal([]byte(s.run("job", "show", job.ID)), &show); err != nil {
		t.Fatal(err)
	}
	if show.Title != "Fix login" || len(show.Plan.Steps) != 2 || show.Plan.Steps[1].ID != "b" {
		t.Fatalf("%+v", show)
	}
	if !strings.Contains(s.run("plan", "show", job.ID), `"blocked_by"`) {
		t.Error("plan show lacks edges")
	}
	if !strings.Contains(s.run("job", "list"), job.ID) {
		t.Error("job list lacks job")
	}
	if out, err := s.cmd("", "job", "show", "nope"); err == nil {
		t.Errorf("missing job succeeded: %s", out)
	}
}

func TestWorksStartsOnceAndOutlivesCommands(t *testing.T) {
	s := newSandbox(t)
	s.run("job", "list") // starts the Works
	pid1 := s.run("works", "status")
	s.run("job", "list")
	if pid2 := s.run("works", "status"); pid1 != pid2 {
		t.Fatalf("works changed:\n%s\n%s", pid1, pid2)
	}
	if !strings.Contains(pid1, `"running": true`) {
		t.Fatalf("not running: %s", pid1)
	}
	// a second explicit start exits cleanly and leaves the instance alone
	s.run("works", "serve")
	if again := s.run("works", "status"); again != pid1 {
		t.Fatalf("works replaced:\n%s\n%s", pid1, again)
	}
}

func TestStateLayoutHonoursOverridesAndIsPrivate(t *testing.T) {
	s := newSandbox(t)
	s.run("job", "create", "--title", "x")
	for _, p := range []string{s.cfg, s.st} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", p, fi, err)
		}
	}
	for _, f := range []string{"archwright.db", "works.pid", "works.lock"} {
		if _, err := os.Stat(filepath.Join(s.st, f)); err != nil {
			t.Errorf("state lacks %s: %v", f, err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(s.st, "works.pid")); fi.Mode().Perm() != 0o600 {
		t.Errorf("pid file mode %v", fi.Mode())
	}
}

func TestXDGDefaultsWithoutOverrides(t *testing.T) {
	root := t.TempDir()
	s := &sandbox{t: t}
	s.env = append(os.Environ(), "ARCHWRIGHT_CONFIG_HOME=", "ARCHWRIGHT_STATE_HOME=",
		"XDG_CONFIG_HOME="+filepath.Join(root, "c"), "XDG_STATE_HOME="+filepath.Join(root, "s"))
	t.Cleanup(func() { s.run("stop") })
	s.run("job", "list")
	if _, err := os.Stat(filepath.Join(root, "s", "archwright", "archwright.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "c", "archwright")); err != nil {
		t.Fatal(err)
	}
}
