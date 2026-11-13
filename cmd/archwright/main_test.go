package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/works"
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

// startRun asks the sandbox's Works for a run, as a runner adapter would, and
// returns the environment the run's agent would inherit.
func (s *sandbox) startRun(role string) works.RunEnv {
	s.t.Helper()
	s.run("job", "list") // ensures the Works is up
	c, ok := works.Running(paths.Dirs{Config: s.cfg, State: s.st})
	if !ok {
		s.t.Fatal("works not running")
	}
	var env works.RunEnv
	if err := c.Do("POST", "/runs", map[string]string{"role": role, "spoolRoot": s.t.TempDir()}, &env); err != nil {
		s.t.Fatal(err)
	}
	return env
}

func (s *sandbox) tool(env works.RunEnv, stdin string, args ...string) (string, int, error) {
	c := exec.Command(binary, append([]string{"tool"}, args...)...)
	c.Env = append(s.env, env.Environ()...)
	c.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return stdout.String(), code, err
}

func TestToolCommandReachesTheWorksThroughTheSpool(t *testing.T) {
	s := newSandbox(t)
	env := s.startRun("archwright")

	out, _, err := s.tool(env, `{"title":"Via spool"}`, "create_job")
	if err != nil {
		t.Fatal(err, out)
	}
	var job struct{ ID, Title string }
	json.Unmarshal([]byte(out), &job)
	if job.Title != "Via spool" {
		t.Fatalf("%s", out)
	}
	// the same Job is visible through the ordinary CLI
	if !strings.Contains(s.run("job", "show", job.ID), "Via spool") {
		t.Fatal("job not in the Works")
	}
	if entries, _ := os.ReadDir(env.Spool); len(entries) != 0 {
		t.Fatalf("spool not cleaned: %v", entries)
	}

	// a tool error is exit 1 with the error JSON on stdout
	out, code, _ := s.tool(env, `{"id":"missing"}`, "show_job")
	if code != 1 || !strings.Contains(out, `"not_found"`) {
		t.Fatalf("code %d: %s", code, out)
	}
	// a role without the tool is refused
	out, code, _ = s.tool(s.startRun("wright"), `{"title":"x"}`, "create_job")
	if code != 1 || !strings.Contains(out, `"forbidden"`) {
		t.Fatalf("code %d: %s", code, out)
	}
}

func TestToolCommandTimeoutExitsWithDistinctCode(t *testing.T) {
	s := newSandbox(t)
	env := works.RunEnv{Run: "run-x", Spool: t.TempDir(), Token: "t"} // no Works serves this spool
	_, code, _ := s.tool(env, ``, "list_jobs", "--wait", "100ms")
	if code != 75 {
		t.Fatalf("exit code %d", code)
	}
	if entries, _ := os.ReadDir(env.Spool); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
	if _, code, _ := s.tool(works.RunEnv{}, ``, "list_jobs"); code != 1 {
		t.Fatalf("outside a run: exit %d", code)
	}
}
