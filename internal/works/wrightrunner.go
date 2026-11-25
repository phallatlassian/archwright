package works

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

// Environment variables that choose how dispatched Wrights and Wardens run. They are the
// developer's or a test's, never an agent's.
const (
	EnvWrightRunner  = "ARCHWRIGHT_WRIGHT_RUNNER" // "fake" runs the scripted fakes instead of real agents
	EnvFakeRunDelay  = "ARCHWRIGHT_FAKE_RUN_DELAY"
	EnvClaudeBinary  = "ARCHWRIGHT_CLAUDE_BIN" // the claude CLI to run, for tests and live checks with a stub
	EnvCodexBinary   = "ARCHWRIGHT_CODEX_BIN"
	EnvTmuxBinary    = "ARCHWRIGHT_TMUX_BIN" // the tmux the factory's panes run in
	wrightRunnerFake = "fake"
)

// WrightRunnerFrom builds the Runner dispatched Wrights run on: interactive
// Claude and Codex Sessions in tmux panes, the only way the factory runs an
// agent (ADR 0026), or the scripted fake runner when the environment asks for
// it. Real runs stay gated by the preflight and the never-list (ADR 0027).
func WrightRunnerFrom(getenv func(string) string, guilds GuildSource, st *store.Store) (Runner, error) {
	if getenv(EnvWrightRunner) == wrightRunnerFake {
		fake := NewFakeRunner(nil)
		if d, err := time.ParseDuration(getenv(EnvFakeRunDelay)); err == nil {
			fake.WithDelay(d) // lets a live check kill the Works while a Wright is in flight
		}
		return fake, nil
	}
	r, err := sessionRunnerFrom(getenv, guilds, st)
	if err != nil { // tmux is a hard dependency (ADR 0026)
		return nil, fmt.Errorf("Wrights cannot run as tmux Sessions: %w", err)
	}
	return r, nil
}

// WardenRunnersFrom builds what task Wardens and review Wardens run on: the
// Sessions of the same SessionRunner as Wrights, so a Job's Sessions share one
// window and one lifecycle (ADR 0029). The fake runner has the scripted fake
// Warden and no review runner.
func WardenRunnersFrom(wrights Runner) (WardenRunner, ReviewRunner) {
	if sr, ok := wrights.(*SessionRunner); ok {
		return sr, sr
	}
	return NewFakeWarden(), nil
}

// sessionRunnerFrom builds the Runner that hosts Claude and Codex Wrights,
// Wardens and reviews as Sessions in the factory tmux session.
func sessionRunnerFrom(getenv func(string) string, guilds GuildSource, st *store.Store) (Runner, error) {
	dirs, err := paths.Resolve(getenv)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	tm := &hosted.Tmux{
		Tmux:        getenv(EnvTmuxBinary),
		Claude:      getenv(EnvClaudeBinary),
		CodexBin:    getenv(EnvCodexBinary),
		HookCommand: []string{exe, "session-hook"},
		Dir:         filepath.Join(dirs.State, "tmux"),
	}
	return &SessionRunner{
		Runners: map[string]hosted.Runner{"claude": tm, "codex": tm.Codex()},
		Guilds:  guilds,
		Record: func(run string, info hosted.Info, state hosted.State) {
			if err := st.SetRunSession(run, sessionRecord(info, state)); err != nil {
				log.Printf("run %s: recording its Session: %v", run, err)
			}
		},
	}, nil
}

// sessionRecord is what the run's record keeps of its Session.
func sessionRecord(info hosted.Info, state hosted.State) store.SessionRecord {
	return store.SessionRecord{PaneID: info.Pane, SessionID: info.SessionID, TranscriptPath: info.TranscriptPath, State: string(state), Dir: info.Dir, Runner: info.Runner}
}

// cliInstalled says whether a runner's CLI is on this machine: the binary the
// environment names for it, or `claude` or `codex` on PATH.
func cliInstalled(getenv func(string) string) func(runner string) bool {
	return func(name string) bool {
		bin, env := name, EnvClaudeBinary
		if name == "codex" {
			env = EnvCodexBinary
		}
		if v := getenv(env); v != "" {
			bin = v
		}
		_, err := exec.LookPath(bin)
		return err == nil
	}
}
