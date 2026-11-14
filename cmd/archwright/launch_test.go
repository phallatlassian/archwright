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

// A fake runner stands in for claude and codex: it records where it was
// started and with what, then does what the runner's hooks and the agent
// would, through the archwright binary. A real interactive session is never launched.
const fakeRunner = `#!/bin/sh
out="$FAKE_OUT"
pwd -P > "$out/cwd"
printf '%s\n' "$@" > "$out/args"
env | grep '^ARCHWRIGHT_' | sort > "$out/env"
if [ -z "$FAKE_NOHOOKS" ]; then
  "$FAKE_ARCHWRIGHT" hook session-start > "$out/session-start"
  "$FAKE_ARCHWRIGHT" hook prompt > "$out/prompt1"
  "$FAKE_ARCHWRIGHT" hook prompt > "$out/prompt2"
else
  "$FAKE_ARCHWRIGHT" tool read_inbox < /dev/null > "$out/polled"
fi
"$FAKE_ARCHWRIGHT" tool list_jobs < /dev/null > "$out/jobs"
ls "$ARCHWRIGHT_SPOOL" > /dev/null && echo "$ARCHWRIGHT_SPOOL" > "$out/spool"
exit ${FAKE_EXIT:-0}
`

type launchEnv struct {
	*sandbox
	out, bin string
}

// newLaunchEnv installs fake claude and codex on PATH and registers a Site
// whose checkout is a clone of its remote.
func newLaunchEnv(t *testing.T) (*launchEnv, string) {
	s := newSandbox(t)
	le := &launchEnv{sandbox: s, out: t.TempDir(), bin: t.TempDir()}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(le.bin, name), []byte(fakeRunner), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hostDir, _ := filepath.EvalSymlinks(t.TempDir())
	s.env = append(s.env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url."+hostDir+"/.insteadOf", "GIT_CONFIG_VALUE_0=https://git.example.com/",
		"PATH="+le.bin+":"+os.Getenv("PATH"), "FAKE_OUT="+le.out, "FAKE_ARCHWRIGHT="+binary)
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
	s.run("site", "register", "--url", "https://git.example.com/o/r.git", "--base-branch", "main", "--name", "demo", "--test", "true")
	checkout := filepath.Join(t.TempDir(), "r")
	c := exec.Command("git", "clone", "https://git.example.com/o/r.git", checkout)
	c.Env = s.env
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return le, checkout
}

// launch runs the bare `archwright` command from dir.
func (le *launchEnv) launch(dir string, extra ...string) (string, error) {
	c := exec.Command(binary)
	c.Dir = dir
	c.Env = append(le.env, extra...)
	out, err := c.CombinedOutput()
	return string(out), err
}

func (le *launchEnv) read(name string) string {
	le.t.Helper()
	b, err := os.ReadFile(filepath.Join(le.out, name))
	if err != nil {
		le.t.Fatal(err)
	}
	return string(b)
}

func TestLaunchStartsRunnerInNeutralHomeWithSiteHintRehydrationAndInbox(t *testing.T) {
	le, checkout := newLaunchEnv(t)
	le.run("job", "create", "--title", "Fix login")
	le.run("job", "list") // Works up
	c, _ := works.Running(paths.Dirs{Config: le.cfg, State: le.st})
	if err := c.Do("POST", "/instruct", map[string]string{"text": "hurry up"}, nil); err != nil {
		t.Fatal(err)
	}

	if out, err := le.launch(checkout); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	home, _ := filepath.EvalSymlinks(filepath.Join(le.st, "home"))
	if got := strings.TrimSpace(le.read("cwd")); got != home {
		t.Fatalf("session started in %q, want the neutral home %q", got, home)
	}
	if fi, _ := os.Stat(home); fi.Mode().Perm() != 0o700 {
		t.Fatalf("home mode %v", fi.Mode().Perm())
	}
	env := le.read("env")
	for _, want := range []string{"ARCHWRIGHT_SITE=demo", "ARCHWRIGHT_RUN=run-", "ARCHWRIGHT_SPOOL=", "ARCHWRIGHT_RUN_TOKEN="} {
		if !strings.Contains(env, want) {
			t.Fatalf("env lacks %q:\n%s", want, env)
		}
	}
	args := le.read("args")
	for _, bad := range []string{"dangerously", "bypass", "--permission-mode", "yolo"} {
		if strings.Contains(args, bad) {
			t.Fatalf("args carry %q:\n%s", bad, args)
		}
	}
	if !strings.Contains(args, "--settings") || !strings.Contains(args, `"hooks"`) || strings.Contains(args, `"permissions"`) {
		t.Fatalf("args:\n%s", args)
	}
	// A fresh session rehydrates from the store, and the inbox arrives by hook once.
	start := le.read("session-start")
	if !strings.Contains(start, "Fix login") || !strings.Contains(start, "hurry up") {
		t.Fatalf("session start context: %s", start)
	}
	if le.read("prompt1") != "" || le.read("prompt2") != "" {
		t.Fatal("the inbox item was delivered at session start and must not repeat")
	}
	if !strings.Contains(le.read("jobs"), "Fix login") {
		t.Fatal("tool call from the session did not reach the Works")
	}
	// The run ends with the session: its spool is gone.
	if _, err := os.Stat(strings.TrimSpace(le.read("spool"))); !os.IsNotExist(err) {
		t.Fatalf("spool outlived the session: %v", err)
	}
}

func TestLaunchFromUnregisteredDirectoryHasNoSiteAndPropagatesExit(t *testing.T) {
	le, _ := newLaunchEnv(t)
	_, err := le.launch(t.TempDir(), "FAKE_EXIT=3")
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Fatalf("exit: %v", err)
	}
	if strings.Contains(le.read("env"), "ARCHWRIGHT_SITE") {
		t.Fatal("unregistered directory must not be a Site")
	}
}

func TestLaunchChosenCodexPollsTheInboxWithoutSettings(t *testing.T) {
	le, checkout := newLaunchEnv(t)
	if err := os.MkdirAll(le.cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(le.cfg, "config.toml"), []byte(`runner = "codex"`+"\n"), 0o600)
	le.run("job", "list")
	c, _ := works.Running(paths.Dirs{Config: le.cfg, State: le.st})
	c.Do("POST", "/instruct", map[string]string{"text": "poll me"}, nil)

	if out, err := le.launch(checkout, "FAKE_NOHOOKS=1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if args := le.read("args"); strings.Contains(args, "--settings") || !strings.Contains(args, "read_inbox") {
		t.Fatalf("codex args:\n%s", args)
	}
	var polled struct{ Items []struct{ Summary string } }
	if err := json.Unmarshal([]byte(le.read("polled")), &polled); err != nil || len(polled.Items) != 1 || !strings.Contains(polled.Items[0].Summary, "poll me") {
		t.Fatalf("polled: %q %v", le.read("polled"), err)
	}
}

func TestLaunchFailsVisiblyWhenChosenRunnerIsMissing(t *testing.T) {
	le, checkout := newLaunchEnv(t)
	os.Remove(filepath.Join(le.bin, "claude"))
	out, err := le.launch(checkout, "PATH="+le.bin)
	if err == nil || !strings.Contains(out, `"claude" is not on PATH`) {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestHookOutsideARunFailsPlainly(t *testing.T) {
	s := newSandbox(t)
	if out, err := s.cmd("", "hook", "prompt"); err == nil || !strings.Contains(out, "not inside a run") {
		t.Fatalf("%v\n%s", err, out)
	}
}
