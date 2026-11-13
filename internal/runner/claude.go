package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Claude runs agents on the stock `claude` CLI headlessly (ADR 0003).
type Claude struct {
	Binary string        // defaults to "claude"; tests point it at a stub
	Grace  time.Duration // how long a cancelled run gets after SIGTERM; default 5s
}

// Name implements Runner.
func (c *Claude) Name() string { return "claude" }

// Capabilities implements Runner. Resume exists in the CLI but is unused in
// v1, so it is not offered (ADR 0003).
func (c *Claude) Capabilities() Capabilities {
	return Capabilities{StructuredOutput: true, Usage: true, PermissionDenied: true}
}

func (c *Claude) bin() string {
	if c.Binary != "" {
		return c.Binary
	}
	return "claude"
}

// args builds the flags of one run. The brief is not among them; every
// flag is fixed here or comes from limits, and the permission mode is
// always passed explicitly so the run never inherits a looser one.
func (c *Claude) args(req StartRequest) (flags []string) {
	flags = []string{"--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-mode", req.Spawn.EffectiveMode()}
	if m := req.Limits.Model; m != "" {
		flags = append(flags, "--model", m)
	}
	if n := req.Limits.MaxTurns; n > 0 {
		flags = append(flags, "--max-turns", strconv.Itoa(n))
	}
	if b := req.Limits.MaxBudgetUSD; b > 0 {
		flags = append(flags, "--max-budget-usd", strconv.FormatFloat(b, 'f', -1, 64))
	}
	return append(flags, req.Spawn.ExtraArgs...)
}

// Start implements Runner. Runs are ephemeral and start with stdin closed.
func (c *Claude) Start(ctx context.Context, req StartRequest) (Run, error) {
	if err := CheckRequest(req); err != nil {
		return nil, err
	}
	flags := c.args(req)
	version := c.version(ctx)

	cmd := exec.Command(c.bin(), append(append([]string{"-p"}, flags...), "--", req.Brief)...) // "--": the brief is never read as a flag
	cmd.Dir = req.Dir
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = nil // /dev/null: nothing can answer a prompt
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	r := &claudeRun{log: newLog(), done: make(chan struct{})}
	r.log.add(newPosture("claude", version, req.Spawn, flags))
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}
	r.cmd = cmd
	grace := c.Grace
	if grace == 0 {
		grace = 5 * time.Second
	}
	go r.pump(ctx, out, &stderr, grace)
	return r, nil
}

// version asks the CLI for its version; a failure is recorded as "unknown"
// rather than stopping the run.
func (c *Claude) version(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.bin(), "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

type claudeRun struct {
	log  *log
	cmd  *exec.Cmd
	done chan struct{}

	mu        sync.Mutex
	cancelled bool
	result    Result
}

func (r *claudeRun) Events() <-chan Event { return r.log.subscribe() }

func (r *claudeRun) Result() Result {
	<-r.done
	return r.result
}

// Cancel stops the whole process group: SIGTERM, then SIGKILL after the grace.
func (r *claudeRun) Cancel() {
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	r.signal(syscall.SIGTERM)
}

func (r *claudeRun) signal(sig syscall.Signal) {
	if r.cmd.Process != nil {
		_ = syscall.Kill(-r.cmd.Process.Pid, sig)
	}
}

func (r *claudeRun) pump(ctx context.Context, out io.Reader, stderr *bytes.Buffer, grace time.Duration) {
	defer close(r.done)
	defer r.log.close()

	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			r.Cancel()
		case <-stop:
			return
		}
		select {
		case <-time.After(grace):
			r.signal(syscall.SIGKILL)
		case <-stop:
		}
	}()

	p := &claudeParser{}
	killed := ""
	rd := bufio.NewReaderSize(out, 1<<20)
	for {
		line, err := rd.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			for _, e := range p.feed(line) {
				r.log.add(e)
			}
			// Fail closed: a session in a mode wider than we asked for is killed.
			if killed == "" && p.sawInit && !permittedModes[p.mode] && p.mode != "" {
				killed = fmt.Sprintf("session reported permission mode %q, which the never-list refuses", p.mode)
				r.signal(syscall.SIGKILL)
			}
		}
		if err != nil {
			break
		}
	}
	waitErr := r.cmd.Wait()
	close(stop)

	r.mu.Lock()
	cancelled := r.cancelled
	r.mu.Unlock()
	res := Result{Status: StatusFailed, Denials: p.denials}
	switch {
	case killed != "":
		res.Error = killed
	case cancelled:
		res.Status = StatusCancelled
		if p.result != nil {
			res.Final, res.Usage = p.result.Final, p.result.Usage
		}
	case p.result != nil:
		res = *p.result
		if waitErr != nil && res.Status == StatusDone {
			res.Status, res.Error = StatusFailed, waitErr.Error()
		}
	default:
		// No final result: fail visibly with whatever the CLI said, nothing retried
		// (a managed policy it cannot parse stops it here).
		res.Error = describeExit(waitErr, stderr.String())
	}
	r.result = res
}

func describeExit(err error, stderr string) string {
	msg := "claude ended without a result"
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg += fmt.Sprintf(" (exit %d)", ee.ExitCode())
	}
	if s := strings.TrimSpace(stderr); s != "" {
		msg += ": " + s
	}
	return msg
}
