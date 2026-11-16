package works

import (
	"log"
	"time"

	"github.com/phallatlassian/archwright/internal/runner"
	"github.com/phallatlassian/archwright/internal/store"
)

// Environment variables that choose how dispatched Wrights and Wardens run. They are the
// developer's or a test's, never an agent's.
const (
	EnvWrightRunner  = "ARCHWRIGHT_WRIGHT_RUNNER" // "fake" runs the scripted fakes instead of real agents
	EnvFakeRunDelay  = "ARCHWRIGHT_FAKE_RUN_DELAY"
	EnvClaudeBinary  = "ARCHWRIGHT_CLAUDE_BIN" // the claude CLI to run, for tests and live checks with a stub
	EnvCodexBinary   = "ARCHWRIGHT_CODEX_BIN"
	wrightRunnerFake = "fake"
)

// WrightRunnerFrom builds the Runner dispatched Wrights run on: real agents on
// the claude or codex runner their Guild is bound to, or the fake runner when
// the environment asks for it. Real runs stay gated by the preflight and the
// adapters' never-list (ADR 0011, 0014).
func WrightRunnerFrom(getenv func(string) string, guilds GuildSource, st *store.Store) Runner {
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
func WardenRunnersFrom(getenv func(string) string, guilds GuildSource, st *store.Store) (WardenRunner, ReviewRunner) {
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
