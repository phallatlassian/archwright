package doctor

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/phallatlassian/archwright/internal/session"
)

// HookResult is what one hook delivery showed.
type HookResult struct {
	Err    string // the hook command could not be run
	Socket bool   // the Works' receiver took the hook over its socket
	Spool  bool   // the hook was written to its spool file, the fallback
}

// HookProber delivers one hook the way a Session's CLI does and says how it travelled.
type HookProber interface {
	Probe(ctx context.Context) HookResult
}

// HookDelivery runs this binary's `session-hook`, the command both CLIs are
// configured to run for every hook, against a receiver and a spool file made
// for the purpose: nothing of a real run is touched. It shows the Works' side
// of hook delivery (the command runs, the socket path binds, the receiver
// authenticates the token, the fallback is written). Whether a vendor CLI
// runs hooks at all is what the trust probe of each Site shows.
type HookDelivery struct {
	Exe     string // the archwright binary
	TempDir string // where the throwaway socket lives; default os.TempDir()
}

// Probe implements HookProber.
func (h HookDelivery) Probe(ctx context.Context) HookResult {
	if h.Exe == "" {
		return HookResult{Err: "this binary's path is unknown"}
	}
	base := h.TempDir
	if base == "" {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "aw-hook")
	if err != nil {
		return HookResult{Err: err.Error()}
	}
	defer os.RemoveAll(dir)
	rcv, err := session.NewReceiver(dir)
	if err != nil {
		return HookResult{Err: err.Error()}
	}
	defer rcv.Close()
	const run, token = "doctor-hook", "doctor-hook-token"
	var got atomic.Bool
	unregister := rcv.Register(run, token, func(session.Delivery) { got.Store(true) })
	defer unregister()

	spoolFile := filepath.Join(dir, "hooks.jsonl")
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.Exe, "session-hook")
	cmd.Env = append(os.Environ(),
		"ARCHWRIGHT_RUN="+run, session.EnvHookToken+"="+token, session.EnvHookFile+"="+spoolFile, session.EnvHookSocket+"="+rcv.Path())
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"doctor","cwd":"/","permission_mode":"default"}`)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return HookResult{Err: err.Error()}
	}
	for deadline := time.Now().Add(2 * time.Second); !got.Load() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := os.ReadFile(spoolFile)
	return HookResult{Socket: got.Load(), Spool: len(b) > 0}
}

// checkHooks: hooks are how the Works learns what a Session is doing. They are
// observe-only and never refuse a run (ADR 0027): when they cannot be
// delivered the factory degrades visibly, so this is a warning with the way to
// look further.
func checkHooks(ctx context.Context, r *collector, d Deps) {
	const fix = "run `archwright session-hook` yourself with a JSON payload on stdin and ARCHWRIGHT_HOOK_FILE set to a file: it must append one line and print nothing. " +
		"Make sure the archwright binary is on PATH and the state directory's sockets can bind (the path stays under 103 bytes). Until then Sessions' state is read from the pane and the transcript, and the dashboard marks it unreliable"
	if d.Hooks == nil {
		return
	}
	res := d.Hooks.Probe(ctx)
	switch {
	case res.Err != "":
		r.warn("hooks", "the hook command could not be run: "+res.Err, fix)
	case res.Socket:
		r.pass("hooks", "a hook run by `archwright session-hook` reached the Works' receiver over its socket, and its spool fallback was written")
	case res.Spool:
		r.warn("hooks", "a hook reached only its spool file, not the Works' socket: state arrives late, through the fallback", fix)
	default:
		r.warn("hooks", "a hook reached neither the Works' socket nor its spool file: it never reached the Works", fix)
	}
}
