// Package doctor is `archwright doctor` (ADR 0014, 0016): read-only checks of
// everything a run depends on, each reporting pass or the exact fix. It never
// writes a settings file, an allow rule or any configuration; it prints what
// the developer should do (ADR 0011). The one thing it writes is the probe's
// short-lived spool directory in each runner's own temp area, removed again.
// A failing report also stops runs from starting (Gate).
package doctor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/works"
)

// Status is the outcome of one check. A warning never blocks runs.
type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Check is one finding: what was looked at, what was seen, and for anything
// that is not a pass the exact fix.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Report is every check, in the order they ran.
type Report struct {
	OK     bool    `json:"ok"` // no check failed; warnings do not count
	Checks []Check `json:"checks"`
}

// Failures are the checks that block runs.
func (r Report) Failures() []Check {
	var out []Check
	for _, c := range r.Checks {
		if c.Status == Fail {
			out = append(out, c)
		}
	}
	return out
}

// Deps are the seams doctor reads the machine through, so tests and live
// checks use stand-ins instead of the developer's real runners.
type Deps struct {
	Dirs     paths.Dirs
	Getenv   func(string) string
	LookPath func(string) (string, error)
	// Output runs a read-only command (a --version or --help check, a git
	// read) and returns its combined output.
	Output func(ctx context.Context, name string, args ...string) (string, error)
	// Probe exercises each installed runner from inside its own sandbox.
	Probe Prober
	// WorksRunning reports a live Works without starting one.
	WorksRunning func() (pid, port int, ok bool)
	Exe          string // this binary, to compare with the archwright on PATH
	UID          int
	TempDir      string
}

// DefaultDeps is doctor on this machine, probing the real runners.
func DefaultDeps(dirs paths.Dirs) Deps {
	exe, _ := os.Executable()
	d := Deps{
		Dirs: dirs, Getenv: os.Getenv, LookPath: exec.LookPath, Output: execOutput, Exe: exe,
		UID: os.Getuid(), TempDir: os.TempDir(),
		WorksRunning: func() (int, int, bool) {
			c, ok := works.Running(dirs)
			if !ok {
				return 0, 0, false
			}
			return c.Info.PID, c.Info.Port, true
		},
	}
	d.Probe = RunnerProber{UID: d.UID, TempDir: d.TempDir}
	return d
}

// execOutput runs a command without prompting for credentials and bounded in time.
func execOutput(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// Run performs every check and returns the report. It changes nothing.
func Run(ctx context.Context, d Deps) Report {
	r := &collector{}
	checkBinary(r, d)
	chosen := checkConfig(r, d)
	checkDirs(r, d)
	snap := checkSchema(r, d)
	if snap != nil {
		defer snap.Close()
	}
	checkWorks(r, d)
	checkSkills(r, d)
	checkLavish(r, d)
	for _, spec := range runnerSpecs {
		checkRunner(ctx, r, d, spec, chosen)
	}
	checkSites(ctx, r, d, snap)

	rep := Report{Checks: r.checks}
	rep.OK = len(rep.Failures()) == 0
	return rep
}

type collector struct{ checks []Check }

func (c *collector) pass(name, format string, a ...any) {
	c.checks = append(c.checks, Check{Name: name, Status: Pass, Detail: fmt.Sprintf(format, a...)})
}

func (c *collector) warn(name, detail, fix string) {
	c.checks = append(c.checks, Check{Name: name, Status: Warn, Detail: detail, Fix: fix})
}

func (c *collector) fail(name, detail, fix string) {
	c.checks = append(c.checks, Check{Name: name, Status: Fail, Detail: detail, Fix: fix})
}

// checkBinary: agents call `archwright tool` by name, so it must be on PATH.
func checkBinary(r *collector, d Deps) {
	found, err := d.LookPath("archwright")
	if err != nil {
		r.fail("archwright-on-path", "`archwright` is not on PATH, so agents cannot run `archwright tool`",
			"build it and put it in a directory on PATH: make build && install bin/archwright ~/.local/bin/ (and add ~/.local/bin to PATH)")
		return
	}
	if d.Exe == "" || sameFile(found, d.Exe) {
		r.pass("archwright-on-path", "%s", found)
		return
	}
	r.warn("archwright-on-path", fmt.Sprintf("PATH finds %s, not this binary (%s); agents will run the former", found, d.Exe),
		fmt.Sprintf("install this build over it: install %s %s", d.Exe, found))
}

func sameFile(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
