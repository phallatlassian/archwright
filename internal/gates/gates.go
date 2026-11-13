// Package gates re-runs a Site's gate commands on a checkout. The commands
// come from the Works registry, never from the checkout, so a branch cannot
// weaken them (ADR 0010).
package gates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

// maxOutput is how much of a gate's output is kept: the tail, where failures are.
const maxOutput = 16 << 10

// DefaultTimeout bounds each gate command.
const DefaultTimeout = 30 * time.Minute

// Run executes the configured gates in dir, in order (type check, lint,
// test), and runs every one even when an earlier gate fails, so the record is
// complete. A gate with no command is skipped.
func Run(ctx context.Context, dir string, g store.Gates, timeout time.Duration) []store.GateResult {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	var out []store.GateResult
	for _, gate := range []struct{ name, command string }{
		{store.GateTypecheck, g.Typecheck}, {store.GateLint, g.Lint}, {store.GateTest, g.Test},
	} {
		out = append(out, runOne(ctx, dir, gate.name, gate.command, timeout))
	}
	return out
}

func runOne(ctx context.Context, dir, name, command string, timeout time.Duration) store.GateResult {
	r := store.GateResult{Gate: name, Command: command, Status: store.GateSkipped}
	if command == "" {
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // the whole process group
	cmd.WaitDelay = 5 * time.Second
	var buf tail
	cmd.Stdout, cmd.Stderr = &buf, &buf
	start := time.Now()
	err := cmd.Run()
	r.DurationMS = time.Since(start).Milliseconds()
	r.Output = buf.String()
	switch {
	case err == nil:
		r.Status = store.GatePassed
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.Status, r.ExitCode = store.GateFailed, -1
		r.Output += fmt.Sprintf("\n[timed out after %s]", timeout)
	default:
		r.Status, r.ExitCode = store.GateFailed, -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.ExitCode = ee.ExitCode()
		} else {
			r.Output += "\n[" + err.Error() + "]"
		}
	}
	return r
}

// Summarize is the overall status of a set of results: failed if any gate
// failed, not_run if none had a command, else passed.
func Summarize(results []store.GateResult) (status, reason string) {
	ran := 0
	for _, r := range results {
		switch r.Status {
		case store.GateFailed:
			return store.GateRunFailed, fmt.Sprintf("%s gate failed", r.Gate)
		case store.GatePassed:
			ran++
		}
	}
	if ran == 0 {
		return store.GateRunNotRun, "the Site has no gate commands configured"
	}
	return store.GateRunPassed, ""
}

// tail keeps the last maxOutput bytes written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > maxOutput {
		t.b = t.b[len(t.b)-maxOutput:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(bytes.ToValidUTF8(t.b, []byte("?"))) }
