package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/doctor"
	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/works"
)

// doctorEnv is a machine for the production (gated) binary: stand-in claude and
// codex scripts, never the real runners, and the binary itself, on a PATH of
// their own.
type doctorEnv struct {
	t        *testing.T
	env      []string
	bin      string
	cfg, st  string
	modeFile string
}

func newDoctorEnv(t *testing.T, mode string, lavish bool) *doctorEnv {
	t.Helper()
	root := t.TempDir()
	e := &doctorEnv{t: t, bin: filepath.Join(root, "bin"), cfg: filepath.Join(root, "cfg"), st: filepath.Join(root, "state"), modeFile: filepath.Join(root, "mode")}
	os.MkdirAll(e.bin, 0o755)
	for name, src := range map[string]string{"claude": "stub-claude.sh", "codex": "stub-codex.sh"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "doctor", "testdata", src))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.bin, name), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(gatedBinary, filepath.Join(e.bin, "archwright")); err != nil {
		t.Fatal(err)
	}
	if lavish {
		os.WriteFile(filepath.Join(e.bin, "lavish-axi"), []byte("#!/bin/sh\n"), 0o755)
	}
	os.WriteFile(e.modeFile, []byte(mode), 0o644)
	gitDir, _ := exec.LookPath("git")
	e.env = []string{"PATH=" + e.bin + ":" + filepath.Dir(gitDir) + ":/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + os.TempDir(),
		"ARCHWRIGHT_CONFIG_HOME=" + e.cfg, "ARCHWRIGHT_STATE_HOME=" + e.st, "STUB_MODE_FILE=" + e.modeFile}
	t.Cleanup(func() { e.cmd("stop") })
	return e
}

func (e *doctorEnv) cmd(args ...string) (string, error) {
	c := exec.Command(gatedBinary, args...)
	c.Env = e.env
	out, err := c.CombinedOutput()
	return string(out), err
}

func (e *doctorEnv) report(args ...string) (doctor.Report, string, error) {
	e.t.Helper()
	c := exec.Command(gatedBinary, args...)
	c.Env = e.env
	var stdout strings.Builder
	var stderr strings.Builder
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	var rep doctor.Report
	if jerr := json.Unmarshal([]byte(stdout.String()), &rep); jerr != nil {
		e.t.Fatalf("doctor output is not a report: %v\n%s\n%s", jerr, stdout.String(), stderr.String())
	}
	return rep, stderr.String(), err
}

func status(rep doctor.Report, name string) doctor.Check {
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	return doctor.Check{Name: name, Status: "absent"}
}

func TestDoctorPassesAndWarnsOnlyForLavish(t *testing.T) {
	e := newDoctorEnv(t, "ok", false)
	rep, _, err := e.report("doctor")
	if err != nil || !rep.OK {
		t.Fatalf("%v: %+v", err, rep.Failures())
	}
	if c := status(rep, "lavish-axi"); c.Status != doctor.Warn || !strings.Contains(c.Fix, "npm install -g lavish-axi") {
		t.Errorf("lavish: %+v", c)
	}
	if c := status(rep, "transport:claude"); c.Status != doctor.Pass {
		t.Errorf("transport: %+v", c)
	}
	rep, _, _ = newDoctorEnv(t, "ok", true).report("doctor")
	if c := status(rep, "lavish-axi"); c.Status != doctor.Pass {
		t.Errorf("lavish installed: %+v", c)
	}
}

func TestDoctorFailsWithTheFixAndExitsNonZero(t *testing.T) {
	e := newDoctorEnv(t, "deny", true)
	rep, stderr, err := e.report("doctor")
	if err == nil || rep.OK {
		t.Fatalf("a denied tool call passed doctor: %+v", rep)
	}
	if c := status(rep, "tool:claude"); c.Status != doctor.Fail || !strings.Contains(c.Fix, "Bash(archwright tool:*)") {
		t.Errorf("%+v", c)
	}
	if !strings.Contains(stderr, "check(s) failed") {
		t.Errorf("stderr: %s", stderr)
	}
}

// Doctor changes nothing and does not start the Works.
func TestDoctorLeavesEveryFileAlone(t *testing.T) {
	e := newDoctorEnv(t, "ok", true)
	if err := os.MkdirAll(e.cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.st, 0o700); err != nil {
		t.Fatal(err)
	}
	e.cmd("job", "list") // a populated state directory, then stop the Works so nothing else writes
	e.cmd("stop")
	snap := func() map[string]string {
		out := map[string]string{}
		for _, root := range []string{e.cfg, e.st} {
			filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
				if err == nil {
					b, _ := os.ReadFile(p)
					out[p] = fi.Mode().String() + fi.ModTime().String() + string(b)
				}
				return nil
			})
		}
		return out
	}
	before := snap()
	for i := 0; i < 100; i++ { // stop returns before the Works has finished closing its files: wait for them to settle
		time.Sleep(100 * time.Millisecond)
		next := snap()
		same := len(next) == len(before)
		for p, v := range before {
			same = same && next[p] == v
		}
		before = next
		if same {
			break
		}
	}
	if _, _, err := e.report("doctor"); err != nil {
		t.Fatal(err)
	}
	after := snap()
	if len(before) != len(after) {
		for p := range before {
			if _, ok := after[p]; !ok {
				t.Logf("vanished: %s", p)
			}
		}
		t.Fatalf("%d files before, %d after", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed", p)
		}
	}
	if _, ok := works.Running(paths.Dirs{Config: e.cfg, State: e.st}); ok {
		t.Error("doctor started the Works")
	}
}

func (e *doctorEnv) startRun() error {
	e.t.Helper()
	if out, err := e.cmd("job", "list"); err != nil { // starts the Works
		e.t.Fatalf("%v\n%s", err, out)
	}
	c, ok := works.Running(paths.Dirs{Config: e.cfg, State: e.st})
	if !ok {
		e.t.Fatal("works not running")
	}
	return c.Do("POST", "/runs", map[string]string{"role": "archwright", "spoolRoot": e.t.TempDir()}, nil)
}

func TestRunsRefuseToStartUntilDoctorPasses(t *testing.T) {
	err := newDoctorEnv(t, "deny", true).startRun()
	if err == nil || !strings.Contains(err.Error(), "runs do not start until it does") || !strings.Contains(err.Error(), "Bash(archwright tool:*)") {
		t.Fatalf("a run started while doctor failed: %v", err)
	}
	if err := newDoctorEnv(t, "ok", true).startRun(); err != nil {
		t.Fatalf("a run was refused while doctor passes: %v", err)
	}
	// a missing lavish-axi is only a warning: it does not block runs
	if err := newDoctorEnv(t, "ok", false).startRun(); err != nil {
		t.Fatalf("a missing lavish-axi blocked a run: %v", err)
	}
}
