// Package launch builds the interactive Archwright session (ADR 0001, 0010,
// 0014): a stock claude or codex started in a neutral factory home, with the
// run's spool in its environment, the Site hint, and, where the runner allows
// it, hooks that deliver the inbox. It never writes a settings file, never
// widens permissions and never touches the Site (ADR 0011).
package launch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/runner"
	"github.com/phallatlassian/archwright/internal/spool"
)

// Runner names the per-install choice.
const (
	RunnerClaude = "claude"
	RunnerCodex  = "codex"
)

// EnvSite carries the Site hint (the registered Site's name) into the session.
const EnvSite = "ARCHWRIGHT_SITE"

// Home is the neutral factory home the Archwright runs in: under the state
// directory, outside every Site, empty of project settings and instructions.
func Home(d paths.Dirs) string { return filepath.Join(d.State, "home") }

// ConfigFile is the per-install configuration the developer edits.
func ConfigFile(d paths.Dirs) string { return filepath.Join(d.Config, "config.toml") }

// ReadRunner returns the install's runner choice: `runner = "claude"` or
// `"codex"` in config.toml, claude when the file or key is absent.
func ReadRunner(d paths.Dirs) (string, error) {
	f, err := os.Open(ConfigFile(d))
	if os.IsNotExist(err) {
		return RunnerClaude, nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	choice := RunnerClaude
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "runner" {
			continue
		}
		choice = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if choice != RunnerClaude && choice != RunnerCodex {
		return "", fmt.Errorf("%s: runner %q must be %q or %q", ConfigFile(d), choice, RunnerClaude, RunnerCodex)
	}
	return choice, nil
}

// SpoolRoot is where the runner's own sandbox can write (ADR 0018).
func SpoolRoot(runnerName string, uid int, tmp string) string {
	if runnerName == RunnerClaude {
		return filepath.Join("/tmp", fmt.Sprintf("claude-%d", uid), "archwright")
	}
	return filepath.Join(tmp, fmt.Sprintf("archwright-%d", uid))
}

// Input is everything a launch is built from.
type Input struct {
	Runner string
	Exe    string // the archwright binary the hooks call
	Home   string
	Site   string // the registered Site's name, or "" when the launch directory is not one
	Run    spool.Env
}

// Command is the process to start.
type Command struct {
	Bin  string
	Args []string
	Dir  string
	Env  []string // additions to the developer's own environment
	// Hooks says whether the inbox is delivered by hooks; otherwise the
	// Archwright polls it through the tool CLI.
	Hooks bool
}

// Build assembles the launch. The only runner flags are the run's hooks
// settings and the instructions; both are checked against the never-list.
func Build(in Input) (Command, error) {
	if in.Runner != RunnerClaude && in.Runner != RunnerCodex {
		return Command{}, fmt.Errorf("unknown runner %q", in.Runner)
	}
	env := map[string]string{spool.EnvRun: in.Run.Run, spool.EnvSpool: in.Run.Dir, spool.EnvToken: in.Run.Token}
	if in.Site != "" {
		env[EnvSite] = in.Site
	}
	if err := runner.CheckRequest(runner.StartRequest{Env: env}); err != nil {
		return Command{}, err
	}
	cmd := Command{Bin: in.Runner, Dir: in.Home}
	for _, k := range []string{spool.EnvRun, spool.EnvSpool, spool.EnvToken, EnvSite} {
		if v, ok := env[k]; ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	switch in.Runner {
	case RunnerClaude:
		// Hooks are the one thing supplied through --settings, and the
		// settings carry nothing else (ADR 0014); managed settings still win.
		settings, err := Settings(in.Exe)
		if err != nil {
			return Command{}, err
		}
		cmd.Hooks = true
		cmd.Args = []string{"--settings", settings, "--append-system-prompt", Instructions(in.Site, true)}
	case RunnerCodex:
		// Codex has no per-run settings channel that is not a config override,
		// so its inbox is polled; the instructions are its first message.
		cmd.Args = []string{"--", Instructions(in.Site, false)}
	}
	return cmd, nil
}

// Settings is the run's settings: hooks that call back into this binary and
// nothing else.
func Settings(exe string) (string, error) {
	hook := func(event string) []map[string]any {
		return []map[string]any{{"hooks": []map[string]any{{"type": "command", "command": shellQuote(exe) + " hook " + event}}}}
	}
	b, err := json.Marshal(map[string]any{"hooks": map[string]any{
		"SessionStart":     hook("session-start"),
		"UserPromptSubmit": hook("prompt"),
		"Stop":             hook("stop"),
	}})
	return string(b), err
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// HooksMarker opens the session-start hook's context; a Claude session that
// does not see it knows its hooks are blocked and polls instead.
const HooksMarker = "[archwright hooks active]"

// InboxMarker opens every inbox delivery.
const InboxMarker = "[archwright inbox]"

// Instructions is what the Archwright is told about itself at launch.
func Instructions(site string, hooks bool) string {
	var b strings.Builder
	b.WriteString("You are the Archwright: the one agent the developer talks to for the whole software factory. ")
	b.WriteString("You run in a neutral factory home, never in a repository. You never read or edit a Site's files and never run git in one; work on a Site happens only through Jobs, Plans and Guilds. ")
	b.WriteString("You keep no state of your own: the Works' store is the memory. Rehydrate now by running `archwright tool list_jobs`, then `archwright tool show_job` with {\"id\": ...} for the Jobs that matter. ")
	b.WriteString("Call every factory tool as `archwright tool NAME` with JSON on stdin and JSON on stdout (create_job, list_jobs, show_job, set_plan, show_plan, read_inbox). ")
	if site != "" {
		fmt.Fprintf(&b, "The developer launched you from the registered Site %q: treat it as the current Site, and give create_job {\"site\": %q} for new work unless they say otherwise. ", site, site)
	} else {
		b.WriteString("The developer's launch directory is not a registered Site, so there is no current Site; ask which Site a Job belongs to. ")
	}
	b.WriteString("Your inbox holds the developer's dashboard commands and reports of ones the Works already ran. ")
	if hooks {
		fmt.Fprintf(&b, "Hooks deliver it: items arrive as context beginning %q, and the session's start context begins %q. ", InboxMarker, HooksMarker)
		fmt.Fprintf(&b, "If you do not see %q in your start context, hooks are blocked here: run `archwright tool read_inbox` at the start of every turn and act on what it returns.", HooksMarker)
	} else {
		b.WriteString("Nothing delivers it for you: run `archwright tool read_inbox` at the start of every turn and act on what it returns.")
	}
	return b.String()
}
