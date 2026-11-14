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
	"strings"
	"sync"
	"syscall"
	"time"
)

// streamParser normalizes one vendor's line-delimited event stream and
// accumulates what the run ends with. It is fed one line at a time.
type streamParser interface {
	feed(line []byte) []Event
	// final is the vendor's final result, nil until it has been seen.
	final() *Result
	denied() []Denial
	// violation is non-empty once the session is seen to run in a posture the
	// never-list refuses; the run is then killed.
	violation() string
}

// noticer is implemented by a parser whose vendor reports managed-policy
// notices at the start of a stream. The run holds its posture event, and what
// follows it, until the notices have all arrived, so the posture records them
// and is still the first event.
type noticer interface {
	// settled reports that no further notices will arrive.
	settled() bool
	notices() []string
}

// procSpec is what an adapter hands startProc: the command line to spawn and
// how to read its output.
type procSpec struct {
	name         string // the runner's name, e.g. "claude"
	bin          string
	argv         []string // arguments after the binary, the brief included
	version      string
	postureFlags []string // the flags recorded in the posture; the brief excluded
	grace        time.Duration
	parser       streamParser
}

// cliVersion asks a vendor CLI for its version; a failure is recorded as
// "unknown" rather than stopping the run.
func cliVersion(ctx context.Context, bin string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// startProc spawns a vendor CLI as a subprocess with stdin closed, in its own
// process group, and pumps its stdout through the parser.
func startProc(ctx context.Context, spec procSpec, req StartRequest) (Run, error) {
	cmd := exec.Command(spec.bin, spec.argv...)
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

	r := &procRun{log: newLog(), done: make(chan struct{}), name: spec.name}
	posture := newPosture(spec.name, spec.version, req.Spawn, spec.postureFlags)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.name, err)
	}
	r.cmd = cmd
	grace := spec.grace
	if grace == 0 {
		grace = 5 * time.Second
	}
	go r.pump(ctx, out, &stderr, grace, spec.parser, posture)
	return r, nil
}

type procRun struct {
	log  *log
	cmd  *exec.Cmd
	done chan struct{}
	name string

	mu        sync.Mutex
	cancelled bool
	result    Result
}

func (r *procRun) Events() <-chan Event { return r.log.subscribe() }

func (r *procRun) Result() Result {
	<-r.done
	return r.result
}

// Cancel stops the whole process group: SIGTERM, then SIGKILL after the grace.
func (r *procRun) Cancel() {
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	r.signal(syscall.SIGTERM)
}

func (r *procRun) signal(sig syscall.Signal) {
	if r.cmd.Process != nil {
		_ = syscall.Kill(-r.cmd.Process.Pid, sig)
	}
}

func (r *procRun) pump(ctx context.Context, out io.Reader, stderr *bytes.Buffer, grace time.Duration, p streamParser, posture Event) {
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

	// A noticer's posture is held back, with the events after it, until settled.
	var held []Event
	n, holding := p.(noticer)
	release := func() {
		if !holding {
			return
		}
		holding = false
		posture.Posture.Notices = append(posture.Posture.Notices, n.notices()...)
		r.log.add(posture)
		for _, e := range held {
			r.log.add(e)
		}
		held = nil
	}
	if !holding {
		r.log.add(posture)
	}
	defer release()

	killed := ""
	rd := bufio.NewReaderSize(out, 1<<20)
	for {
		line, err := rd.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			for _, e := range p.feed(line) {
				if holding {
					held = append(held, e)
				} else {
					r.log.add(e)
				}
			}
			if holding && n.settled() {
				release()
			}
			if v := p.violation(); killed == "" && v != "" {
				killed = v
				r.signal(syscall.SIGKILL)
			}
		}
		if err != nil {
			break
		}
	}
	waitErr := r.cmd.Wait()
	close(stop)
	release()

	r.mu.Lock()
	cancelled := r.cancelled
	r.mu.Unlock()
	final := p.final()
	res := Result{Status: StatusFailed, Denials: p.denied()}
	switch {
	case killed != "":
		res.Error = killed
	case cancelled:
		res.Status = StatusCancelled
		if final != nil {
			res.Final, res.Usage = final.Final, final.Usage
		}
	case final != nil:
		res = *final
		if waitErr != nil && res.Status == StatusDone {
			res.Status, res.Error = StatusFailed, waitErr.Error()
		}
	default:
		// No final result: fail visibly with whatever the CLI said, nothing retried
		// (a managed policy it cannot parse stops it here).
		res.Error = describeExit(r.name, waitErr, stderr.String())
	}
	r.result = res
}

func describeExit(name string, err error, stderr string) string {
	msg := name + " ended without a result"
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg += fmt.Sprintf(" (exit %d)", ee.ExitCode())
	}
	if s := strings.TrimSpace(stderr); s != "" {
		msg += ": " + s
	}
	return msg
}
