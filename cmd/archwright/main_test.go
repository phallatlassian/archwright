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

func TestGateChangesNeedApprovalAtATerminal(t *testing.T) {
	s := newSandbox(t)
	// A git host stands in for the remote: insteadOf maps this URL onto a bare repository.
	hostDir, _ := filepath.EvalSymlinks(t.TempDir())
	s.env = append(s.env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url."+hostDir+"/.insteadOf", "GIT_CONFIG_VALUE_0=https://git.example.com/")
	bare := filepath.Join(hostDir, "o", "r.git")
	work, _ := filepath.EvalSymlinks(t.TempDir())
	for _, args := range [][]string{
		{"init", "--bare", "-b", "main", bare},
		{"-C", work, "init", "-b", "main"},
		{"-C", work, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "init"},
		{"-C", work, "push", bare, "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	s.run("site", "register", "--url", "https://git.example.com/o/r.git", "--base-branch", "main", "--name", "demo", "--test", "go test ./...")

	var ch struct{ ID, Status string }
	if err := json.Unmarshal([]byte(s.run("site", "gates", "demo", "--test", "true")), &ch); err != nil || ch.Status != "pending" {
		t.Fatalf("%+v %v", ch, err)
	}
	if out, err := s.cmd("", "site", "gates", "demo"); err == nil {
		t.Fatalf("proposed nothing: %s", out)
	}
	// no terminal on stdin: even "y" is refused, and nothing changes
	if out, err := s.cmd("y\n", "site", "gate-approve", ch.ID); err == nil || !strings.Contains(out, "terminal") {
		t.Fatalf("approved without a terminal: %v\n%s", err, out)
	}
	if out := s.run("site", "list"); !strings.Contains(out, "go test ./...") || strings.Contains(out, `"test": "true"`) {
		t.Fatalf("gates changed:\n%s", out)
	}
	if out := s.run("site", "gate-changes", "--status", "pending"); !strings.Contains(out, ch.ID) {
		t.Fatalf("%s", out)
	}
	s.run("site", "gate-reject", ch.ID)
	if out := s.run("site", "gate-changes", "--status", "pending"); strings.Contains(out, ch.ID) {
		t.Fatalf("%s", out)
	}
}

// cmdWith runs the binary with extra environment entries and, if env is not
// empty, as the run it describes.
func (s *sandbox) cmdWith(extra []string, stdin string, args ...string) (string, error) {
	c := exec.Command(binary, args...)
	c.Env = append(append([]string(nil), s.env...), extra...)
	c.Stdin = strings.NewReader(stdin)
	out, err := c.Output()
	return string(out), err
}

func (s *sandbox) startRunFor(role, job, guild string) works.RunEnv {
	s.t.Helper()
	s.run("job", "list")
	c, _ := works.Running(paths.Dirs{Config: s.cfg, State: s.st})
	var env works.RunEnv
	if err := c.Do("POST", "/runs", map[string]string{"role": role, "spoolRoot": s.t.TempDir(), "job": job, "guild": guild}, &env); err != nil {
		s.t.Fatal(err)
	}
	return env
}

type askView struct {
	ID, Status string
	AnsweredBy string `json:"answered_by"`
	Answers    []struct {
		QuestionID string `json:"question_id"`
		Values     []string
		Skipped    bool
	}
}

func (s *sandbox) ask(id string) askView {
	s.t.Helper()
	var shown struct{ Ask askView }
	if err := json.Unmarshal([]byte(s.run("ask", "show", id)), &shown); err != nil {
		s.t.Fatal(err)
	}
	return shown.Ask
}

func TestAsksReachTheDeveloperThroughLavishOrPlainText(t *testing.T) {
	s := newSandbox(t)
	var job struct{ ID string }
	json.Unmarshal([]byte(s.run("job", "create", "--title", "Add login")), &job)
	warden := s.startRunFor("warden", job.ID, "engineering")
	arch := s.startRun("archwright")

	// A stand-in lavish-axi that logs how it was called.
	bin, logFile := t.TempDir(), filepath.Join(t.TempDir(), "lavish.log")
	script := "#!/bin/sh\necho \"$@\" >> " + logFile + "\n"
	if err := os.WriteFile(filepath.Join(bin, "lavish-axi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	withLavish := []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
	noLavish := []string{"PATH=/usr/bin:/bin"}
	lavishLog := func() string { b, _ := os.ReadFile(logFile); return string(b) }

	raise := func(text string) string {
		out, _, err := s.tool(warden, `{"kind":"decision","blocking":true,"why":"needs you","questions":[{"id":"scheme","text":"`+text+`","options":["jwt","session"],"default":"session"}]}`, "ask")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		var r struct {
			AskID string `json:"ask_id"`
		}
		json.Unmarshal([]byte(out), &r)
		return r.AskID
	}
	record := func(env works.RunEnv, extra []string, id, submission string) map[string]any {
		c := exec.Command(binary, "ask", "record", id)
		c.Env = append(append(append([]string(nil), s.env...), env.Environ()...), extra...)
		c.Stdin = strings.NewReader(submission)
		out, err := c.Output()
		if err != nil {
			t.Fatalf("record: %v\n%s", err, out)
		}
		var m map[string]any
		json.Unmarshal(out, &m)
		return m
	}

	// 1. Lavish: the page is opened and the submitted answer is recorded through the tool API.
	a1 := raise("Which scheme?")
	out, err := s.cmdWith(withLavish, "", "ask", "page", a1)
	if err != nil || !strings.Contains(out, `"mode": "lavish"`) {
		t.Fatalf("%v\n%s", err, out)
	}
	page := filepath.Join(s.st, "asks", a1+".html")
	if b, err := os.ReadFile(page); err != nil || !strings.Contains(string(b), "Which scheme?") || !strings.Contains(string(b), `value="jwt"`) {
		t.Fatalf("page: %v", err)
	}
	if !strings.Contains(lavishLog(), page) {
		t.Fatalf("lavish-axi was not asked to open the page: %q", lavishLog())
	}
	res := record(arch, withLavish, a1, `{"ask_id":"`+a1+`","action":"answer","answers":[{"question_id":"scheme","values":["jwt"]}]}`)
	if res["recorded"] != true || res["page_ended"] != true || !strings.Contains(lavishLog(), "end "+page) {
		t.Fatalf("%v\n%s", res, lavishLog())
	}
	if a := s.ask(a1); a.Status != "answered" || a.AnsweredBy != "archwright" || a.Answers[0].Values[0] != "jwt" {
		t.Fatalf("%+v", a)
	}

	// 2. The dashboard answered first: the page's later answer is rejected by the Works and the page is ended.
	a2 := raise("Which scheme again?")
	if _, err := s.cmdWith(withLavish, "", "ask", "page", a2); err != nil {
		t.Fatal(err)
	}
	s.run("ask", "accept-default", a2)
	page2 := filepath.Join(s.st, "asks", a2+".html")
	res = record(arch, withLavish, a2, `{"ask_id":"`+a2+`","action":"answer","answers":[{"question_id":"scheme","values":["jwt"]}]}`)
	if res["recorded"] != false || res["page_ended"] != true || !strings.Contains(lavishLog(), "end "+page2) || res["tell_developer"] == nil {
		t.Fatalf("%v\n%s", res, lavishLog())
	}
	if a := s.ask(a2); a.AnsweredBy != "dashboard" || a.Answers[0].Values[0] != "session" {
		t.Fatalf("the first answer must stand: %+v", a)
	}

	// 3. Without lavish-axi the same ask is plain text and can still be answered.
	a3 := raise("Which scheme, in text?")
	out, err = s.cmdWith(noLavish, "", "ask", "page", a3)
	if err != nil || !strings.Contains(out, `"mode": "text"`) || !strings.Contains(out, "Which scheme, in text?") || !strings.Contains(out, "recommended default: session") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := s.cmd(`{"answers":[{"question_id":"scheme","values":["jwt"]}]}`, "ask", "answer", a3); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if a := s.ask(a3); a.Status != "answered" || a.Answers[0].Values[0] != "jwt" {
		t.Fatalf("%+v", a)
	}
	// a second answer on the other surface loses
	if out, err := s.cmd("", "ask", "skip", a3); err == nil {
		t.Fatalf("a second answer was accepted: %s", out)
	}
	if out, err := s.cmd("", "ask", "page", a3); err == nil {
		t.Fatalf("an answered ask was put to the developer again: %s", out)
	}

	// 4. Skip and cancel are developer actions too; cancel discards the Job.
	a4 := raise("Skip me")
	s.run("ask", "skip", a4)
	if a := s.ask(a4); !a.Answers[0].Skipped {
		t.Fatalf("%+v", a)
	}
	a5 := raise("Cancel me")
	s.run("ask", "cancel", a5)
	if a := s.ask(a5); a.Status != "cancelled" {
		t.Fatalf("%+v", a)
	}
	if out := s.run("job", "show", job.ID); !strings.Contains(out, `"outcome": "discarded"`) {
		t.Fatalf("%s", out)
	}
}
