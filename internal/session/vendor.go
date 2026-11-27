package session

import (
	"context"
	"time"
)

// vendor is what differs between the CLIs the tmux adapter hosts: the launch
// line, where the hooks come from, and what can be said to a running Session.
// Everything after the launch (the hook receiver, the state machine, the pane's
// liveness, cancel) is the same for both.
type vendor struct {
	name string
	// checkMode refuses a permission mode the vendor cannot honour, before
	// anything is written or spawned.
	checkMode func(mode string) error
	// plan prepares what the launch needs on disk and says how to start the CLI.
	plan func(t *Tmux, req LaunchRequest, runDir, prompt string) (launchPlan, error)
	// wrap is the Session handed to the caller: the vendor's own capabilities on top of the pane's.
	wrap func(*tmuxSession) Session
}

// launchPlan is one launch of a vendor CLI.
type launchPlan struct {
	bin     string            // the CLI binary, for its version
	argv    []string          // the whole command, bin first
	flags   []string          // the flags the Posture reports
	env     map[string]string // what the vendor needs in its pane beyond the hook wiring
	notices []string          // what the developer should know about this launch
}

var vendorClaude = vendor{
	name:      "claude",
	checkMode: func(string) error { return nil }, // the never-list already admits only default, plan and dontAsk
	plan:      claudePlan,
	wrap:      func(s *tmuxSession) Session { return s },
}

// claudePlan: hooks through a --settings file, merged with the managed ones,
// and a session id the Works chooses.
func claudePlan(t *Tmux, req LaunchRequest, runDir, prompt string) (launchPlan, error) {
	settings, err := t.writeSettings(runDir)
	if err != nil {
		return launchPlan{}, err
	}
	bin := t.or(t.Claude, "claude")
	flags := []string{"--permission-mode", req.Spawn.EffectiveMode(), "--settings", settings}
	if req.Resume != "" {
		flags = append(flags, "--resume", req.Resume) // the session id stays the one resumed
	} else {
		flags = append(flags, "--session-id", newUUID())
	}
	if m := req.Limits.Model; m != "" {
		flags = append(flags, "--model", m)
	}
	flags = append(flags, req.Spawn.ExtraArgs...)
	return launchPlan{bin: bin, argv: append(append([]string{bin}, flags...), "--", prompt), flags: flags}, nil // "--": the brief is never read as a flag
}

// vendors are the vendor CLIs the adapter can host, by the name a Session's
// record gives.
var vendors = map[string]vendor{"claude": vendorClaude, "codex": vendorCodex}

// Codex returns the Runner for Codex Sessions on the same tmux server,
// factory session and hook receiver as t's Claude Sessions.
func (t *Tmux) Codex() Runner { return codexRunner{t} }

type codexRunner struct{ t *Tmux }

// UseBaseEnv implements BaseEnvUser: Codex Sessions share the server and its environment.
func (c codexRunner) UseBaseEnv(f func() map[string]string) { c.t.UseBaseEnv(f) }

// FocusIfIdle implements Focuser.
func (c codexRunner) FocusIfIdle(pane string, idle time.Duration) (bool, error) {
	return c.t.FocusIfIdle(pane, idle)
}

// Name implements Runner.
func (c codexRunner) Name() string { return "codex" }

// Capabilities implements Runner.
func (c codexRunner) Capabilities() Capabilities {
	return Capabilities{Hooks: true, PermissionRequests: true, Queue: true, Resume: true}
}

// Adopt implements Adopter: Codex Sessions share the factory, so a pane is
// adopted as whatever vendor it was launched as.
func (c codexRunner) Adopt(pane string, runs ...string) (Session, error) {
	return c.t.Adopt(pane, runs...)
}

// FlagOrphans implements Orphaner.
func (c codexRunner) FlagOrphans(known map[string]bool) ([]Orphan, error) {
	return c.t.FlagOrphans(known)
}

// Launch implements Runner.
func (c codexRunner) Launch(ctx context.Context, req LaunchRequest) (Session, error) {
	return c.t.launch(ctx, req, vendorCodex)
}
