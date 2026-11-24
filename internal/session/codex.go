package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phallatlassian/archwright/internal/runner"
)

// codexHookEvents are the hooks the Works observes in a Codex Session. Codex has
// no Notification.
var codexHookEvents = []string{
	HookSessionStart, HookUserPromptSubmit, HookPreToolUse, HookPermissionReq,
	HookPostToolUse, HookStop, HookSessionEnd,
}

// codexShared are what a Codex Session takes from the developer's own Codex
// home: the login, the config (with the developer's own trust), the rules and
// the standing instructions. Everything else in the home is the Works'.
var codexShared = []string{"auth.json", "config.toml", "rules", "AGENTS.md"}

const codexQueueTimeout = 15 * time.Second

var vendorCodex = vendor{
	name:      "codex",
	checkMode: codexMode,
	plan:      codexPlan,
	wrap:      func(s *tmuxSession) Session { return codexSession{s} },
}

// codexMode: Codex has no permission mode flag, and the never-list forbids the
// approval and sandbox flags that would stand in for one, so a Session runs at
// its default; a narrower mode cannot be enforced and fails the launch.
func codexMode(mode string) error {
	if mode != runner.ModeDefault {
		return fmt.Errorf("codex has no %q permission mode: a Session runs at its default", mode)
	}
	return nil
}

func (t *Tmux) codexBin() string { return t.or(t.CodexBin, "codex") }

// codexHome is the CODEX_HOME of every Codex Session. It is the same path for
// every run, because Codex keys its trust of a hook by the file it came from
// and a hash of its definition: a hooks file written per run, or hooks passed
// with -c (keyed by position, overwriting each other), would ask the developer
// again each time.
func (t *Tmux) codexHome() string { return t.or(t.CodexHome, filepath.Join(t.Dir, "codex-home")) }

func (t *Tmux) userCodexHome() string {
	if t.UserCodexHome != "" {
		return t.UserCodexHome
	}
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex")
	}
	return ""
}

// codexPlan: hooks from the stable file in the Works' Codex home, the
// worktree as Codex's root, the brief as the first prompt. Codex picks its own
// session id; the SessionStart hook reports it.
func codexPlan(t *Tmux, req LaunchRequest, _, prompt string) (launchPlan, error) {
	home := t.codexHome()
	hooksFile, err := t.prepareCodexHome(home)
	if err != nil {
		return launchPlan{}, fmt.Errorf("preparing the Codex home: %w", err)
	}
	bin := t.codexBin()
	flags := []string{"-C", req.Dir, "--no-alt-screen"} // inline mode keeps the pane readable to capture-pane
	if m := req.Limits.Model; m != "" {
		flags = append(flags, "-m", m)
	}
	flags = append(flags, req.Spawn.ExtraArgs...)
	return launchPlan{
		bin: bin, argv: append(append([]string{bin}, flags...), "--", prompt), flags: flags,
		env:     map[string]string{"CODEX_HOME": home},
		notices: []string{fmt.Sprintf("hooks come from %s; Codex asks the developer to trust them once, by that file", hooksFile)},
	}, nil
}

// prepareCodexHome makes the Works' Codex home: the hooks file, written only
// when its content changes so that its path and hash stay what the developer
// trusted, and links to what is shared with the developer's own Codex home.
func (t *Tmux) prepareCodexHome(home string) (hooksFile string, err error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	want, err := t.hooksJSON(codexHookEvents)
	if err != nil {
		return "", err
	}
	hooksFile = filepath.Join(home, "hooks.json")
	if have, err := os.ReadFile(hooksFile); err != nil || !bytes.Equal(have, want) {
		tmp, err := os.CreateTemp(home, ".hooks-*.json")
		if err != nil {
			return "", err
		}
		_, werr := tmp.Write(want)
		if cerr := tmp.Close(); werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Rename(tmp.Name(), hooksFile)
		}
		if werr != nil {
			os.Remove(tmp.Name())
			return "", werr
		}
	}
	user := t.userCodexHome()
	if user == "" || filepath.Clean(user) == filepath.Clean(home) {
		return hooksFile, nil
	}
	for _, name := range codexShared {
		src, dst := filepath.Join(user, name), filepath.Join(home, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			continue // already linked, or the developer's Codex has since replaced it with its own file
		}
		if err := os.Symlink(src, dst); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return hooksFile, nil
}

// codexSession is a Codex Session: a pane's Session that can also be messaged
// while it works.
type codexSession struct{ *tmuxSession }

// Queue implements Queuer with `codex queue`, addressed by the session id the
// SessionStart hook reported. A Codex Session that is working queues the text
// and delivers it as a new turn after its Stop; the turn's UserPromptSubmit
// then reports it. It is never typed into the pane, where a prompt would take
// it as an answer (docs/spec/tmux-runtime.md).
func (c codexSession) Queue(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("nothing to queue")
	}
	if st := c.State(); st != StateWorking {
		return fmt.Errorf("%w: the Session is %s, not working", ErrNotQueueable, st)
	}
	id := c.Info().SessionID
	if id == "" {
		return fmt.Errorf("%w: its session id is not known yet", ErrNotQueueable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexQueueTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.t.codexBin(), "queue", "--thread", id, "--message", text)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+c.t.codexHome())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("codex queue: %w: %s", err, oneLine(string(out)))
	}
	return nil
}
