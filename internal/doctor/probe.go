package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phallatlassian/archwright/internal/launch"
	"github.com/phallatlassian/archwright/internal/runner"
	"github.com/phallatlassian/archwright/internal/spool"
)

// ProbeResult is what one probe run of a runner showed.
type ProbeResult struct {
	Err          string // the probe could not run to the end
	Denied       []runner.Denial
	RequestSeen  bool // the call's request reached the spool, with the probe's token
	ResponseRead bool // the agent got the response back (its nonce appears in the run)
	Notices      []string
}

// Prober exercises one installed runner from inside its sandbox. It is a
// seam: tests and live checks use stand-in runner scripts through it.
type Prober interface {
	Probe(ctx context.Context, name, bin string) ProbeResult
}

// ProbeTool is the tool the probe calls; only the doctor's own responder knows it.
const ProbeTool = "doctor_ping"

// RunnerProber probes by starting a short headless run through the runner's
// adapter: the adapter carries the never-list, so the probe cannot widen
// permissions, and the run is in dontAsk mode so anything that would prompt
// is denied and reported rather than waiting. The run is asked to make one
// `archwright tool doctor_ping` call, which the prober answers itself from a
// spool directory it creates in the runner's spool root and removes after:
// no Works is started and nothing persistent is written.
type RunnerProber struct {
	UID     int
	TempDir string
	Timeout time.Duration // default 3 minutes
}

const probeBrief = "This is a self-check of an Archwright installation, not a task. " +
	"Run exactly this one shell command and nothing else: archwright tool " + ProbeTool + "\n" +
	"Then reply with only the JSON it printed. Do not run any other command and do not edit any file."

// Probe implements Prober.
func (p RunnerProber) Probe(ctx context.Context, name, bin string) ProbeResult {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var rn runner.Runner
	switch name {
	case launch.RunnerClaude:
		rn = &runner.Claude{Binary: bin}
	case launch.RunnerCodex:
		rn = &runner.Codex{Binary: bin}
	default:
		return ProbeResult{Err: "unknown runner " + name}
	}

	root := launch.SpoolRoot(name, p.UID, p.TempDir)
	_, statErr := os.Stat(root)
	createdRoot := errors.Is(statErr, fs.ErrNotExist)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return ProbeResult{Err: "cannot create the spool root " + root + ": " + err.Error()}
	}
	dir, err := os.MkdirTemp(root, "doctor-")
	if err != nil {
		return ProbeResult{Err: "cannot create a probe spool under " + root + ": " + err.Error()}
	}
	work, err := os.MkdirTemp("", "archwright-doctor-")
	if err != nil {
		os.RemoveAll(dir)
		return ProbeResult{Err: err.Error()}
	}
	defer func() {
		os.RemoveAll(work)
		os.RemoveAll(dir)
		if createdRoot {
			os.Remove(root) // only if it is empty again
		}
	}()

	token, nonce := spool.NewID(), spool.NewID()
	var seen atomic.Bool
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); respond(dir, token, nonce, &seen, stop) }()
	defer func() { close(stop); wg.Wait() }()

	run, err := rn.Start(ctx, runner.StartRequest{
		Brief: probeBrief, Dir: work,
		Env:   map[string]string{spool.EnvRun: "doctor-probe", spool.EnvSpool: dir, spool.EnvToken: token},
		Spawn: runner.Spawn{Mode: runner.ModeDontAsk},
	})
	if err != nil {
		return ProbeResult{Err: err.Error()}
	}
	go func() { <-ctx.Done(); run.Cancel() }()

	var res ProbeResult
	var sawNonce bool
	for ev := range run.Events() {
		if ev.Posture != nil {
			res.Notices = append(res.Notices, ev.Posture.Notices...)
		}
		if ev.Kind == runner.KindPermissionDenied {
			res.Denied = append(res.Denied, runner.Denial{Tool: ev.Tool, Command: ev.Command, ToolUseID: ev.ToolUseID})
		}
		if ev.Kind == runner.KindToolResult && (strings.Contains(ev.Text, nonce) || strings.Contains(string(ev.Raw), nonce)) {
			sawNonce = true
		}
	}
	out := run.Result()
	if len(res.Denied) == 0 {
		res.Denied = out.Denials
	}
	if strings.Contains(out.Final, nonce) {
		sawNonce = true
	}
	res.RequestSeen, res.ResponseRead = seen.Load(), sawNonce
	if out.Status == runner.StatusCancelled {
		res.Err = fmt.Sprintf("timed out after %s", timeout)
	} else if out.Status == runner.StatusFailed && len(res.Denied) == 0 && !res.RequestSeen {
		res.Err = out.Error
	}
	return res
}

// respond answers the probe's call: a request carrying the probe's token for
// ProbeTool gets the nonce back; anything else is refused.
func respond(dir, token, nonce string, seen *atomic.Bool, stop <-chan struct{}) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			id, ok := spool.RequestID(e.Name())
			if !ok {
				continue
			}
			req, err := spool.ReadRequest(dir, id)
			if err != nil || req.ID != id {
				continue
			}
			resp := spool.Response{ID: id, Error: &spool.Error{Code: spool.CodeUnauthorized, Message: "not the doctor probe"}}
			if req.Token == token && req.Tool == ProbeTool {
				out, _ := json.Marshal(map[string]string{"pong": nonce})
				resp = spool.Response{ID: id, OK: true, Output: out}
				seen.Store(true)
			}
			spool.WriteResponse(dir, resp)
			os.Remove(spool.RequestPath(dir, id))
		}
		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}
