package works

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/runner"
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
	// wrightRunnerTmux runs Wrights as interactive Sessions in tmux panes
	// (ADR 0026). It is opt-in until the headless runners are removed.
	wrightRunnerTmux = "tmux"
)

// WrightRunnerFrom builds the Runner dispatched Wrights run on: real agents on
// the claude or codex runner their Guild is bound to, or the fake runner when
// the environment asks for it, or, when it asks for tmux, interactive Claude
// and Codex Sessions in tmux panes. Real runs stay gated by the preflight and
// the adapters' never-list (ADR 0011, 0014, 0027).
func WrightRunnerFrom(getenv func(string) string, guilds GuildSource, st *store.Store) Runner {
	if getenv(EnvWrightRunner) == wrightRunnerTmux {
		r, err := sessionRunnerFrom(getenv, guilds, st)
		if err != nil { // tmux is a hard dependency with no headless fallback (ADR 0026)
			return failingRunner{fmt.Errorf("Wrights cannot run as tmux Sessions: %w", err)}
		}
		return r
	}
	if getenv(EnvWrightRunner) == wrightRunnerFake {
		fake := NewFakeRunner(nil)
		if d, err := time.ParseDuration(getenv(EnvFakeRunDelay)); err == nil {
			fake.WithDelay(d) // lets a live check kill the Works while a Wright is in flight
		}
		return fake
	}
	return agentRunnerFrom(getenv, guilds, st)
}

// WardenRunnersFrom builds what task Wardens and review Wardens run on: real
// agents on the runner their Guild is bound to, like Wrights, or, when the
// environment asks for the fake runner, the scripted fake Warden with no
// review runner. The posture, never-list and preflight are the Wrights'.
// wrights is the Runner built for Wrights: when it hosts Sessions, Wardens and
// reviews are Sessions of the same runner, so a Job's Sessions share one window
// and one lifecycle (ADR 0029).
func WardenRunnersFrom(getenv func(string) string, guilds GuildSource, st *store.Store, wrights Runner) (WardenRunner, ReviewRunner) {
	if sr, ok := wrights.(*SessionRunner); ok {
		return sr, sr
	}
	if getenv(EnvWrightRunner) == wrightRunnerFake {
		return NewFakeWarden(), nil
	}
	w := &AgentWarden{Agent: agentRunnerFrom(getenv, guilds, st)}
	return w, w
}

func agentRunnerFrom(getenv func(string) string, guilds GuildSource, st *store.Store) *AgentRunner {
	return &AgentRunner{
		Runners: map[string]runner.Runner{
			"claude": &runner.Claude{Binary: getenv(EnvClaudeBinary)},
			"codex":  &runner.Codex{Binary: getenv(EnvCodexBinary)},
		},
		Guilds: guilds,
		Denied: func(req RunRequest, denials []runner.Denial) {
			job, err := st.GetJob(req.Job)
			if err == nil {
				_, err = RaisePermissionsWanted(st, job.ID, job.Site, denials)
			}
			if err != nil {
				log.Printf("run %s: filing permission wanted: %v", req.Env.Run, err)
			}
		},
	}
}

// failingRunner fails every run with its error, so a Runner that could not be
// built is seen at the dispatch rather than hidden behind another one.
type failingRunner struct{ err error }

func (f failingRunner) Run(context.Context, RunRequest) error { return f.err }

// sessionRunnerFrom builds the Runner that hosts Claude and Codex Wrights as
// Sessions in the factory tmux session, with the headless runner behind it for
// any runner without a Session adapter.
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
		Runners:  map[string]hosted.Runner{"claude": tm, "codex": tm.Codex()},
		Guilds:   guilds,
		Fallback: agentRunnerFrom(getenv, guilds, st),
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
