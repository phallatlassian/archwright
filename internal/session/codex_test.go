package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/runner"
)

func TestCodexLaunchesWithTheWorktreeAsRootAndTheBriefAfterDoubleDash(t *testing.T) {
	r := newTmuxRig(t, "codex")
	brief := "Fix it; don't `rm -rf $HOME`\nsecond \"line\" with --flags"
	req := r.request(t, scenTurn)
	req.Brief, req.Limits = brief, Limits{Model: "gpt-x"}
	s := r.launch(t, req)
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })

	args := r.saw(t, "args")
	for _, want := range []string{"-C\n" + req.Dir + "\n", "--no-alt-screen\n", "-m\ngpt-x\n", "\n--\n" + brief + "\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	for _, bad := range []string{"--permission-mode", "--settings", "--session-id", "bypass", "dangerously", "yolo", "-s\n", "-c\n", "-a\n"} {
		if strings.Contains(args, bad) {
			t.Errorf("args contain %q:\n%s", bad, args)
		}
	}
	if pwd, want := strings.TrimSpace(r.saw(t, "pwd")), mustReal(t, req.Dir); pwd != want {
		t.Errorf("ran in %q, want the worktree %q", pwd, want)
	}
	env := r.saw(t, "env")
	for _, want := range []string{"CODEX_HOME=" + r.tm.codexHome(), "ARCHWRIGHT_RUN=" + req.Run, EnvHookSocket + "=", EnvHookFile + "=", EnvHookToken + "="} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
	// Codex picks its own session id, and the hooks are how the Works learns it.
	if id := s.Info().SessionID; !strings.HasPrefix(id, "codex-") {
		t.Errorf("session id = %q", id)
	}
}

func TestCodexHooksComeFromOneStableFileThatIsNotRewrittenForARun(t *testing.T) {
	r := newTmuxRig(t, "codex")
	first := r.launch(t, r.request(t, scenTurn))
	waitFor(t, "a turn", func() bool { return first.Info().Turns == 1 })
	file := filepath.Join(r.tm.codexHome(), "hooks.json")
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(file)

	second := r.launch(t, r.request(t, scenTurn))
	waitFor(t, "a turn", func() bool { return second.Info().Turns == 1 })
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Error("the hooks file was replaced for a second run: its trust is keyed by file and hash")
	}
	if again, _ := os.ReadFile(file); string(again) != string(content) {
		t.Error("the hooks file's content changed between runs")
	}
	// Nothing of a run is in the file: the run's wiring travels in the environment.
	if strings.Contains(string(content), "run-") {
		t.Errorf("the hooks file names a run:\n%s", content)
	}
	if entries, _ := filepath.Glob(filepath.Join(r.tm.Dir, "runs", "*", "settings.json")); len(entries) != 0 {
		t.Errorf("a Codex run wrote a per-run settings file: %v", entries)
	}
}

func TestCodexHooksOnlyObserveAndOmitNotification(t *testing.T) {
	r := newTmuxRig(t, "codex")
	s := r.launch(t, r.request(t, scenTurn))
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	b, err := os.ReadFile(filepath.Join(r.tm.codexHome(), "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]map[string][]struct {
		Hooks []struct{ Type, Command string }
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(b, &raw)
	if len(raw) != 1 || raw["hooks"] == nil {
		t.Fatalf("hooks file keys = %v: only hooks may be set", raw)
	}
	if len(file["hooks"]) != len(codexHookEvents) {
		t.Errorf("hooks = %v", file["hooks"])
	}
	for _, e := range codexHookEvents {
		groups := file["hooks"][e]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 || groups[0].Hooks[0].Type != "command" || !strings.Contains(groups[0].Hooks[0].Command, os.Args[0]) {
			t.Errorf("hook %s = %+v", e, groups)
		}
	}
	if _, ok := file["hooks"][HookNotification]; ok {
		t.Error("Codex has no Notification hook")
	}
}

func TestCodexHomeSharesTheDevelopersLoginAndConfigWithoutOverwritingAnything(t *testing.T) {
	r := newTmuxRig(t, "codex")
	user := r.tm.UserCodexHome
	if err := os.MkdirAll(filepath.Join(user, "rules"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"auth.json", "config.toml"} {
		if err := os.WriteFile(filepath.Join(user, f), []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A hooks.json of the developer's own is never linked, so never replaced or shared.
	if err := os.WriteFile(filepath.Join(user, "hooks.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file the Works' home already has is left alone.
	home := r.tm.codexHome()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("works own"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := r.launch(t, r.request(t, scenTurn))
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })

	for name, want := range map[string]string{"auth.json": filepath.Join(user, "auth.json"), "rules": filepath.Join(user, "rules")} {
		if got, err := os.Readlink(filepath.Join(home, name)); err != nil || got != want {
			t.Errorf("%s -> %q (%v), want a link to %q", name, got, err, want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home, "config.toml")); string(b) != "works own" {
		t.Errorf("the Works' own config.toml was replaced: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(home, "AGENTS.md")); err == nil {
		t.Error("linked a file the developer does not have")
	}
	if b, _ := os.ReadFile(filepath.Join(user, "hooks.json")); string(b) != `{"hooks":{}}` {
		t.Errorf("the developer's hooks.json was touched: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "hooks.json")); strings.Contains(string(b), `"hooks":{}`) || !strings.Contains(string(b), "SessionStart") {
		t.Errorf("the Works' hooks file is not its own: %q", b)
	}
}

func TestCodexPostureNamesTheHooksFileAndReportsDefaultMode(t *testing.T) {
	r := newTmuxRig(t, "codex")
	s := r.launch(t, r.request(t, scenTurn))
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	s.Cancel()
	p := collect(t, s)[0].Posture
	if p == nil || p.Runner != "codex" || p.Mode != "default" || p.Sandbox != runner.SandboxInherited {
		t.Fatalf("posture = %+v", p)
	}
	if !strings.HasPrefix(p.Version, "codex-cli") {
		t.Errorf("version = %q", p.Version)
	}
	if len(p.Notices) != 1 || !strings.Contains(p.Notices[0], filepath.Join(r.tm.codexHome(), "hooks.json")) {
		t.Errorf("notices = %v", p.Notices)
	}
}

func TestCodexRefusesAModeItCannotEnforceBeforeSpawningAnything(t *testing.T) {
	r := newTmuxRig(t, "codex")
	for _, mode := range []string{"plan", "dontAsk"} {
		req := r.request(t, scenTurn)
		req.Spawn = runner.Spawn{Mode: mode}
		s, err := r.runner().Launch(context.Background(), req)
		if err == nil || s != nil || !strings.Contains(err.Error(), "no \""+mode+"\" permission mode") {
			t.Fatalf("mode %s: session = %v, err = %v", mode, s, err)
		}
	}
	if _, err := os.Stat(r.tm.codexHome()); err == nil {
		t.Error("a refused launch prepared the Codex home")
	}
	if out, _ := r.tm.tmux("list-panes", "-a"); out != "" {
		t.Errorf("a refused launch left panes: %q", out)
	}
}

func TestCodexQueueRefusesWhileWaitingOnAPromptAndBeforeTheSessionIDIsKnown(t *testing.T) {
	r := newTmuxRig(t, "codex")
	s := r.launch(t, r.request(t, scenPermHold))
	waitState(t, s, StateWaitingOnPermission)
	if err := s.(Queuer).Queue("hello"); err == nil || !strings.Contains(err.Error(), "waiting-on-permission") {
		t.Fatalf("queue at a prompt = %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.tm.codexHome(), "queue-args")); err == nil {
		t.Error("codex queue was run against a Session at a prompt")
	}
	if err := s.(Queuer).Queue("  "); err == nil {
		t.Error("an empty message was queued")
	}

	idle := r.launch(t, r.request(t, scenTurn))
	waitFor(t, "a turn", func() bool { return idle.Info().Turns == 1 })
	if err := idle.(Queuer).Queue("hello"); err == nil || !strings.Contains(err.Error(), "not working") {
		t.Fatalf("queue to an idle Session = %v", err)
	}
}

func TestClaudeSessionsAreNotQueueable(t *testing.T) {
	r := newTmuxRig(t, "claude")
	s := r.launch(t, r.request(t, scenTurn))
	if _, ok := s.(Queuer); ok {
		t.Fatal("a Claude Session claims a queue it does not have")
	}
	if r.tm.Capabilities().Queue || !r.tm.Codex().Capabilities().Queue {
		t.Fatal("capabilities disagree about the queue")
	}
}
