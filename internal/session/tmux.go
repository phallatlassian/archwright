package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/phallatlassian/archwright/internal/envsnap"
	"github.com/phallatlassian/archwright/internal/runner"
)

// Defaults of the tmux adapter.
const (
	DefaultSocketName  = "archwright" // tmux -L: the Works' own server, never the developer's
	DefaultSessionName = "factory"
	archwrightWindow   = "archwright"
	// RoleArchwright is the LaunchRequest.Role of the Archwright, whose pane is
	// placed in the `archwright` window rather than a Job's.
	RoleArchwright      = "archwright"
	DefaultStartTimeout = 60 * time.Second // no SessionStart hook within this fails the Session "failed to start"
	DefaultCancelGrace  = 3 * time.Second
	defaultPoll         = 200 * time.Millisecond
	// maxInlineBrief is the longest brief passed as the positional argument.
	// tmux carries a command to its server in one 16 KB message, so a longer
	// brief is written to a file and the prompt points at it.
	maxInlineBrief = 6000
	// DefaultNudgeTimeout is how long a Nudge waits for the UserPromptSubmit hook that confirms it.
	DefaultNudgeTimeout = 15 * time.Second
	// DefaultNudgePasteDelay separates the paste from the Enter: sent together,
	// Codex can leave the text in its composer (docs/spec/tmux-runtime.md).
	DefaultNudgePasteDelay = 200 * time.Millisecond
	// maxNudgeInline is the longest single-line Nudge pasted as it is; a longer
	// one, or one over several lines, is written to a file and a pointer pasted.
	maxNudgeInline = 1000
	// endedGrace is how long a Session whose SessionEnd hook has fired gets to
	// exit before its result is taken from what is known.
	endedGrace = 2 * time.Second
)

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// hookEvents are the hooks the Works observes in a Claude Session; Codex sends
// all but Notification (codexHookEvents).
var hookEvents = []string{
	HookSessionStart, HookUserPromptSubmit, HookPreToolUse, HookPermissionReq,
	HookPostToolUse, HookNotification, HookStop, HookSessionEnd,
}

// Tmux hosts interactive Sessions in panes of a tmux server it owns: one
// `factory` session, a window per Job, a pane per Session (ADR 0026,
// docs/spec/tmux-runtime.md). It is the Runner for Claude; Codex() is the same
// server and factory seen as the Runner for Codex. The zero value needs only
// Dir and HookCommand.
type Tmux struct {
	Tmux        string // the tmux binary; default "tmux"
	Socket      string // the server's socket path (tmux -S); wins over SocketName
	SocketName  string // the server's socket name (tmux -L); default DefaultSocketName
	SessionName string // default DefaultSessionName
	Claude      string // the claude binary; default "claude"; tests point it at a stand-in
	CodexBin    string // the codex binary; default "codex"
	// HookCommand is the argv of what a hook runs: `archwright session-hook`.
	HookCommand []string
	// Dir holds the Works' files for the factory: its tmux config and each run's
	// hook settings, hook spool and long brief.
	Dir string
	// SocketDir holds the hook receiver's socket. Its path must stay under the
	// platform's unix socket limit, so it defaults to a short one under the
	// temp directory, not Dir.
	SocketDir string
	// CodexHome is the CODEX_HOME every Codex Session runs with: the stable,
	// Works-owned place of the hooks file Codex trusts by path. Default
	// <Dir>/codex-home.
	CodexHome string
	// UserCodexHome is the developer's own Codex home, whose login, config and
	// rules a Codex Session shares; default $CODEX_HOME, else ~/.codex.
	UserCodexHome string
	// UserConf is the developer's own tmux config, sourced before the Works';
	// "" means ~/.tmux.conf and "-" none.
	UserConf string
	// ThemePlugin is the tmux-ukiyo plugin file the factory's status line is
	// themed with when it exists; "" means ~/.tmux/plugins/tmux-ukiyo/ukiyo.tmux
	// and "-" none.
	ThemePlugin string
	// BaseEnv, when set, is the developer's shell environment as the Works
	// snapshotted it (envsnap): the tmux server is started with exactly that and
	// every Session is spawned with the current snapshot. The Works' own
	// environment goes no further. Unset, Sessions inherit the server's, which
	// is the Works' own: only tests run that way.
	BaseEnv      func() map[string]string
	StartTimeout time.Duration
	CancelGrace  time.Duration
	Poll         time.Duration
	// NudgeTimeout bounds the wait for the hook that confirms a Nudge, and
	// NudgePasteDelay is the pause between the paste and the Enter.
	NudgeTimeout    time.Duration
	NudgePasteDelay time.Duration

	mu       sync.Mutex // serialises server and window creation
	recv     *Receiver
	versions map[string]string
}

// BaseEnvUser is a Runner that takes the developer's shell environment from a
// source the Works keeps current, instead of inheriting the Works' own.
type BaseEnvUser interface {
	UseBaseEnv(func() map[string]string)
}

// UseBaseEnv implements BaseEnvUser. Call it before the first launch.
func (t *Tmux) UseBaseEnv(f func() map[string]string) { t.BaseEnv = f }

// Name implements Runner.
func (t *Tmux) Name() string { return "claude" }

// Capabilities implements Runner.
func (t *Tmux) Capabilities() Capabilities {
	return Capabilities{Hooks: true, PermissionRequests: true, Resume: true}
}

// Close stops the hook receiver. Sessions are left running in their panes: the
// tmux server outlives the Works.
func (t *Tmux) Close() {
	t.mu.Lock()
	r := t.recv
	t.recv = nil
	t.mu.Unlock()
	if r != nil {
		r.Close()
	}
}

func (t *Tmux) or(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func (t *Tmux) dur(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

func (t *Tmux) session() string { return t.or(t.SessionName, DefaultSessionName) }

func (t *Tmux) socketArgs() []string {
	if t.Socket != "" {
		return []string{"-S", t.Socket}
	}
	return []string{"-L", t.or(t.SocketName, DefaultSocketName)}
}

// tmux runs one tmux command against the Works' server and returns its output.
func (t *Tmux) tmux(args ...string) (string, error) { return t.tmuxRun("", nil, args...) }

// tmuxIn is tmux with stdin as the command's standard input.
func (t *Tmux) tmuxIn(stdin string, args ...string) (string, error) {
	return t.tmuxRun(stdin, nil, args...)
}

// tmuxRun is tmux with stdin as the command's standard input and, when env is
// not nil, exactly that environment: a command that starts the server gives
// the server its environment.
func (t *Tmux) tmuxRun(stdin string, env []string, args ...string) (string, error) {
	cmd := exec.Command(t.or(t.Tmux, "tmux"), append(t.socketArgs(), args...)...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// Launch implements Runner. ctx bounds the launch, not the Session: a Session
// outlives the call and ends through Cancel, its pane, or its own SessionEnd.
func (t *Tmux) Launch(ctx context.Context, req LaunchRequest) (Session, error) {
	return t.launch(ctx, req, vendorClaude)
}

func (t *Tmux) launch(ctx context.Context, req LaunchRequest, v vendor) (Session, error) {
	if err := checkLaunch(req); err != nil {
		return nil, err
	}
	if err := v.checkMode(req.Spawn.EffectiveMode()); err != nil {
		return nil, err
	}
	if t.Dir == "" || len(t.HookCommand) == 0 {
		return nil, errors.New("the tmux adapter has no Dir or HookCommand configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runDir := filepath.Join(t.Dir, "runs", req.Run)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, err
	}
	token := randomHex(16)
	hookFile := filepath.Join(runDir, "hooks.jsonl")
	if err := os.WriteFile(hookFile, nil, 0o600); err != nil {
		return nil, err
	}
	if err := writeMeta(runDir, sessionMeta{Run: req.Run, Token: token, Vendor: v.name, Job: req.Job, Role: req.Role, Dir: req.Dir}); err != nil {
		return nil, err
	}
	prompt, err := t.promptFor(runDir, req.Brief)
	if err != nil {
		return nil, err
	}
	plan, err := v.plan(t, req, runDir, prompt)
	if err != nil {
		return nil, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureReceiverLocked(); err != nil {
		return nil, err
	}
	env := map[string]string{}
	if t.BaseEnv != nil {
		env = envsnap.Filter(t.BaseEnv()) // the allow-list holds however the snapshot was made
	}
	for k, v := range req.Env {
		env[k] = v
	}
	// The hook wiring is the adapter's: a caller's environment cannot redirect it.
	env[envRun], env[EnvHookSocket], env[EnvHookFile], env[EnvHookToken] = req.Run, t.recv.Path(), hookFile, token
	for k, val := range plan.env {
		env[k] = val
	}
	pane, window, err := t.place(req, env, plan.argv)
	if err != nil {
		return nil, err
	}
	s := &tmuxSession{t: t, v: v, startWithin: t.dur(req.StartTimeout, t.dur(t.StartTimeout, DefaultStartTimeout)), core: newCore(token), pane: pane, runDir: runDir, hookFile: hookFile, cancelCh: make(chan struct{}), started: time.Now()}
	s.core.setPane(pane, window)
	s.core.setPlace(req.Dir, v.name)
	s.core.add(Event{Kind: KindPosture, Posture: &runner.Posture{
		Runner: v.name, Version: t.cliVersion(plan.bin), Mode: req.Spawn.EffectiveMode(), Sandbox: runner.SandboxInherited,
		Flags: plan.flags, Notices: append([]string{}, plan.notices...),
	}})
	s.core.onState = s.markState
	unregister := t.recv.Register(req.Run, token, s.core.deliver)
	for opt, val := range map[string]string{"@run": req.Run, "@role": req.Role, "@guild": req.Guild, "@state": stateLabel(StateStarting)} {
		if _, err := t.tmux("set-option", "-p", "-t", pane, opt, val); err != nil {
			t.tmux("kill-pane", "-t", pane)
			unregister()
			return nil, err
		}
	}
	if req.Role != RoleArchwright {
		t.arrange(window) // after the options: the grid is built from them
	}
	go func() {
		defer unregister()
		s.run()
	}()
	return v.wrap(s), nil
}

// ensureReceiverLocked starts the hook receiver if it is not running. The
// caller holds t.mu.
func (t *Tmux) ensureReceiverLocked() error {
	if t.recv != nil {
		return nil
	}
	r, err := NewReceiver(t.or(t.SocketDir, filepath.Join(os.TempDir(), fmt.Sprintf("aw-%d", os.Getuid()))))
	if err != nil {
		return fmt.Errorf("hook receiver: %w", err)
	}
	t.recv = r
	return nil
}

// checkLaunch refuses what the never-list forbids (ADR 0027) and what a pane
// cannot be named or placed for.
func checkLaunch(req LaunchRequest) error {
	if err := runner.CheckRequest(req.Env, req.Spawn); err != nil {
		return err
	}
	switch {
	case !runIDPattern.MatchString(req.Run):
		return fmt.Errorf("run id %q is not a plain id", req.Run)
	case req.Dir == "":
		return errors.New("the Session has no working directory")
	case req.Brief == "":
		return errors.New("the Session has no brief")
	}
	return nil
}

// writeSettings writes the hooks the Works observes, and nothing else: no
// permission, allow rule or sandbox key, and every hook command only reports
// (ADR 0027). Claude merges it with the managed settings.
func (t *Tmux) writeSettings(runDir string) (string, error) {
	b, err := t.hooksJSON(hookEvents)
	if err != nil {
		return "", err
	}
	path := filepath.Join(runDir, "settings.json")
	return path, os.WriteFile(path, b, 0o600)
}

// hooksJSON is the hooks block both vendors read: each event runs the hook
// command, which only reports. The bytes depend on nothing of a run, so the
// file Codex trusts by path stays the same across runs.
func (t *Tmux) hooksJSON(events []string) ([]byte, error) {
	hook := map[string]any{"type": "command", "command": shellJoin(t.HookCommand)}
	hooks := map[string][]any{}
	for _, e := range events {
		hooks[e] = []any{map[string]any{"hooks": []any{hook}}}
	}
	b, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// promptFor is the first prompt: the brief, or for a long one a pointer to a file holding it.
func (t *Tmux) promptFor(runDir, brief string) (string, error) {
	if len(brief) <= maxInlineBrief {
		return brief, nil
	}
	path := filepath.Join(runDir, "brief.md")
	if err := os.WriteFile(path, []byte(brief), 0o600); err != nil {
		return "", err
	}
	return "Your brief is in the file " + path + ". Read all of it, then carry it out.", nil
}

// place puts the command in a pane of the Job's window, creating the factory
// session and the window as needed, and returns the pane and window ids. The
// command is passed as argv, so tmux runs it without a shell and the brief
// needs no quoting.
func (t *Tmux) place(req LaunchRequest, env map[string]string, argv []string) (pane, window string, err error) {
	if err := t.ensureServer(); err != nil {
		return "", "", err
	}
	var envArgs []string
	for k, v := range env {
		envArgs = append(envArgs, "-e", k+"="+v)
	}
	name := req.Job
	if req.Role == RoleArchwright {
		name = archwrightWindow // the Archwright's window, not a Job's (ADR 0026)
	} else if name == "" {
		name = "work"
	}
	const format = "#{pane_id} #{window_id}"
	// A Job's window disappears with its last pane, so the window found can be
	// gone by the time the split runs: look again and, with no window, make one.
	var out string
	for attempt := 0; ; attempt++ {
		window, err = t.findWindow(name)
		if err != nil {
			return "", "", err
		}
		var args []string
		if window == "" {
			args = []string{"new-window", "-d", "-t", t.session() + ":", "-n", name, "-c", req.Dir, "-P", "-F", format}
		} else {
			args = []string{"split-window", "-d", "-t", window, "-c", req.Dir, "-P", "-F", format}
		}
		out, err = t.tmux(append(append(append(args, envArgs...), "--"), argv...)...)
		if err == nil {
			break
		}
		if window == "" || attempt >= 2 || !strings.Contains(err.Error(), "can't find window") {
			return "", "", err
		}
	}
	pane, window, ok := strings.Cut(out, " ")
	if !ok {
		return "", "", fmt.Errorf("tmux gave no pane for the Session: %q", out)
	}
	if req.Role == RoleArchwright {
		t.clearArchwrightWindow(window, pane)
	}
	return pane, window, nil
}

// clearArchwrightWindow removes what the Archwright's window held before its
// Session: the empty shell pane the factory session was created with, and the
// dead pane of an Archwright that ended. keep is the new Session's pane.
func (t *Tmux) clearArchwrightWindow(window, keep string) {
	out, err := t.tmux("list-panes", "-t", window, "-F", "#{pane_id} #{pane_dead} #{@run}")
	if err != nil {
		return
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 || f[0] == keep {
			continue
		}
		if f[1] == "1" || len(f) < 3 { // dead, or never a Session: no @run
			t.tmux("kill-pane", "-t", f[0])
		}
	}
}

// ensureServer starts the Works' tmux server with the factory session if it
// is not running. The config is Works-supplied: the developer's own first, then
// what the factory requires (docs/spec/tmux-runtime.md).
func (t *Tmux) ensureServer() error {
	if _, err := t.tmux("has-session", "-t", "="+t.session()); err == nil {
		// A server from an earlier Works keeps its options; the factory still needs this one.
		t.tmux("set-option", "-g", "remain-on-exit", "on")
		return nil
	}
	conf, err := t.writeConf()
	if err != nil {
		return err
	}
	// The server is the parent of every pane, so it is started with the
	// snapshot and not the Works' own environment.
	var env []string
	if t.BaseEnv != nil {
		env = append([]string{}, envsnap.Environ(envsnap.Filter(t.BaseEnv()))...) // not nil: an empty snapshot is an empty environment
	}
	// -x/-y: a detached session defaults to 80x24, too small to split.
	_, err = t.tmuxRun("", env, "-f", conf, "new-session", "-d", "-s", t.session(), "-n", archwrightWindow, "-x", "200", "-y", "50")
	return err
}

func (t *Tmux) writeConf() (string, error) {
	conf := t.UserConf
	if conf == "" {
		if home, err := os.UserHomeDir(); err == nil {
			conf = filepath.Join(home, ".tmux.conf")
		}
	}
	if err := os.MkdirAll(t.Dir, 0o700); err != nil {
		return "", err
	}
	var b strings.Builder
	if conf != "" && conf != "-" {
		fmt.Fprintf(&b, "source-file -q %s\n", tmuxQuote(conf))
	}
	b.WriteString("# set by the Works, after the developer's own config\n")
	b.WriteString("set-option -g remain-on-exit on\n") // a dead pane keeps its final screen readable
	if err := t.writeTheme(&b); err != nil {           // before the factory's own: a theme sets pane borders too
		return "", err
	}
	b.WriteString(factoryConf)
	path := filepath.Join(t.Dir, "tmux.conf")
	return path, os.WriteFile(path, []byte(b.String()), 0o600)
}

// factoryConf is the factory's look: the prefix, a label on every pane's
// border built from its pane options (guild, role, run, state), and a border
// colour by role. The label is "guild · role · run · state" with a warning mark
// while a permission is waited on; a pane with no @role (the Archwright's) shows
// its title; the active pane's label is filled and bold with a marker, the others dim.
const factoryConf = `unbind C-b
set-option -g prefix C-a
bind-key C-a send-prefix
set-option -g pane-border-status top
set-option -gwq pane-border-lines double
set-option -g pane-border-format '#{?pane_active,#[reverse bold] ▶ #{?#{&&:#{@role},#{!=:#{@role},archwright}},#{@guild} · #{@role} · #{?@run,#{@run},empty}#{?@state, · #{?#{==:#{@state},permission},⚠ ,}#{@state},},#{pane_title}} #[default],#[dim] #{?#{&&:#{@role},#{!=:#{@role},archwright}},#{@guild} · #{@role} · #{?@run,#{@run},empty}#{?@state, · #{?#{==:#{@state},permission},⚠ ,}#{@state},},#{pane_title}} #[default]}'
set-option -g pane-border-style 'fg=#{?@placeholder,colour240,#{?#{==:#{@role},archwright},colour141,#{?#{==:#{@role},warden},colour208,#{?@role,colour39,colour141}}}},bg=default'
set-option -g pane-active-border-style 'fg=#{?@placeholder,colour240,#{?#{==:#{@role},archwright},colour141,#{?#{==:#{@role},warden},colour208,#{?@role,colour39,colour141}}}},bg=default'
`

// writeTheme writes the status line: tmux-ukiyo's nord theme when its plugin is
// installed, else tmux's own. The plugin is run directly, never through tpm,
// which would also read the developer's config and load their other plugins.
// Either way window tabs read "[0] Name" and the right of the status line is empty.
func (t *Tmux) writeTheme(b *strings.Builder) error {
	plugin := t.ThemePlugin
	if plugin == "" {
		if home, err := os.UserHomeDir(); err == nil {
			plugin = filepath.Join(home, ".tmux", "plugins", "tmux-ukiyo", "ukiyo.tmux")
		}
	}
	if st, err := os.Stat(plugin); plugin != "" && plugin != "-" && err == nil && !st.IsDir() {
		tabs := filepath.Join(t.Dir, "tabs.sh")
		script := "#!/usr/bin/env bash\n# Rewrite the theme's window tabs from \"#I #W\" to \"[#I] #W\", keeping its colors.\n" +
			"tmux=" + shellJoin([]string{t.or(t.Tmux, "tmux")}) + "\n" +
			"for o in window-status-format window-status-current-format; do\n" +
			"  v=$(\"$tmux\" show-option -gwv \"$o\")\n" +
			"  \"$tmux\" set-option -gw \"$o\" \"${v//#I #W/[#I] #W}\"\n" +
			"done\n"
		if err := os.WriteFile(tabs, []byte(script), 0o700); err != nil {
			return err
		}
		fmt.Fprintf(b, "set-option -g @ukiyo-theme \"nord/default\"\nrun-shell %s\nrun-shell %s\n", tmuxQuote(plugin), tmuxQuote("bash "+shellJoin([]string{tabs})))
	} else {
		b.WriteString("set-option -gw window-status-format '[#I] #W'\nset-option -gw window-status-current-format '[#I] #W'\n")
	}
	b.WriteString("set-option -g status-right ''\n") // after the theme, which fills it
	return nil
}

// stateLabel is a Session's state as the pane's @state option, and so its border label, shows it.
func stateLabel(s State) string {
	if s == StateWaitingOnPermission {
		return "permission"
	}
	return string(s)
}

// markState records the Session's state in its pane's @state option, which the
// border label shows. A pane that is gone is not an error.
func (s *tmuxSession) markState(st State) {
	s.t.tmux("set-option", "-p", "-t", s.pane, "@state", stateLabel(st))
}

// findWindow returns the id of the Job's window, or "" if it has none.
func (t *Tmux) findWindow(name string) (string, error) {
	out, err := t.tmux("list-windows", "-t", t.session()+":", "-F", "#{window_id} #{window_name}")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(out, "\n") {
		if id, n, ok := strings.Cut(l, " "); ok && n == name {
			return id, nil
		}
	}
	return "", nil
}

// cliVersion is what `<bin> --version` prints, once per binary; "unknown" if it
// cannot say. The caller holds t.mu.
func (t *Tmux) cliVersion(bin string) string {
	if v, ok := t.versions[bin]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	version := "unknown"
	if err == nil {
		if v := oneLine(string(out)); v != "" {
			version = v
		}
	}
	if t.versions == nil {
		t.versions = map[string]string{}
	}
	t.versions[bin] = version
	return version
}

// tmuxSession is one Session in one pane.
type tmuxSession struct {
	t        *Tmux
	v        vendor
	core     *core
	pane     string
	runDir   string
	hookFile string
	started  time.Time
	// startWithin is how long the Session may take to send its first SessionStart hook.
	startWithin time.Duration

	nudgeMu sync.Mutex // one Nudge at a time: two pastes must not interleave
	nudges  int        // Nudges that wrote a file, for its name

	cancelCh   chan struct{}
	cancelOnce sync.Once

	spoolOff int64
	spoolBuf []byte
}

func (s *tmuxSession) Info() Info           { return s.core.Info() }
func (s *tmuxSession) State() State         { return s.core.State() }
func (s *tmuxSession) Events() <-chan Event { return s.core.Events() }
func (s *tmuxSession) Result() Result       { return s.core.Result() }

// Nudge implements Session. The text is loaded into a tmux buffer and pasted,
// and after a short pause Enter is sent; the UserPromptSubmit hook confirms
// the agent took it (docs/spec/tmux-runtime.md). Nothing is sent unless the
// Session is idle with no PermissionRequest outstanding, and that is checked
// again between the paste and the Enter: Enter at a prompt approves it.
func (s *tmuxSession) Nudge(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("nothing to nudge")
	}
	s.nudgeMu.Lock()
	defer s.nudgeMu.Unlock()
	if err := s.core.nudgeable(); err != nil {
		return err
	}
	prompt, err := s.nudgePrompt(text)
	if err != nil {
		return err
	}
	before := s.core.prompts()
	buf := "aw-nudge-" + strings.TrimPrefix(s.pane, "%")
	if _, err := s.t.tmuxIn(prompt, "load-buffer", "-b", buf, "-"); err != nil {
		return err
	}
	// -p: bracketed paste where the CLI asked for it, so a line break in the text is not an Enter; -d drops the buffer.
	if _, err := s.t.tmux("paste-buffer", "-d", "-p", "-b", buf, "-t", s.pane); err != nil {
		_, _ = s.t.tmux("delete-buffer", "-b", buf)
		return err
	}
	time.Sleep(s.t.dur(s.t.NudgePasteDelay, DefaultNudgePasteDelay))
	if err := s.core.nudgeable(); err != nil {
		// Not ErrNotIdle: the text is in the composer now, so trying again would paste it twice.
		return fmt.Errorf("%w: the Session stopped being idle after the paste (%v); Enter was not sent", ErrNudgeUnconfirmed, err)
	}
	s.core.expectPrompt() // the prompt this Enter submits is the Works', not a human's
	if _, err := s.t.tmux("send-keys", "-t", s.pane, "Enter"); err != nil {
		s.core.retractPrompt()
		return err
	}
	return s.core.awaitPrompt(before, s.t.dur(s.t.NudgeTimeout, DefaultNudgeTimeout))
}

// nudgePrompt is what is pasted for a Nudge: the text itself when it is one
// short line a CLI will not read as a command, otherwise a pointer to a file
// holding it, so the exact text stays in the Works' record (ADR 0029).
func (s *tmuxSession) nudgePrompt(text string) (string, error) {
	text = strings.TrimSpace(text)
	if !strings.Contains(text, "\n") && len(text) <= maxNudgeInline && !strings.ContainsAny(text[:1], "/!#@&") {
		return text, nil
	}
	s.nudges++
	path := filepath.Join(s.runDir, fmt.Sprintf("nudge-%d.md", s.nudges))
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return "Read the file " + path + " in full and carry out what it says.", nil
}

// Cancel implements Session. A Session that has already ended has no loop left
// to do it, so its pane, which remain-on-exit keeps, is removed here.
func (s *tmuxSession) Cancel() {
	s.cancelOnce.Do(func() {
		close(s.cancelCh)
		if s.core.isFinished() {
			s.t.tmux("kill-pane", "-t", s.pane) // already gone is fine
		}
	})
}

// run follows the Session until it ends: hooks arrive on their own through the
// receiver; this adds the spool fallback, the start deadline and the pane's liveness.
func (s *tmuxSession) run() {
	t := s.t
	tick := time.NewTicker(t.dur(t.Poll, defaultPoll))
	defer tick.Stop()
	startBy := s.started.Add(s.startWithin)
	var endedAt time.Time
	lost := 0
	for {
		select {
		case <-s.cancelCh:
			s.cancel()
			return
		case <-tick.C:
		}
		s.drainSpool()
		dead, status, err := s.paneState()
		switch {
		case err != nil:
			if lost++; lost >= 3 { // one failed query is a busy server; three is a lost pane
				s.core.finish(Result{Status: runner.StatusFailed, ExitStatus: -1, Error: "the pane is gone: " + err.Error()})
				return
			}
			continue
		case dead:
			s.drainSpool() // hooks written just before the exit
			s.finishDead(status)
			return
		}
		lost = 0
		switch st := s.core.State(); {
		case st == StateEnded: // SessionEnd fired; the process should follow
			if endedAt.IsZero() {
				endedAt = time.Now()
			}
			if time.Since(endedAt) > endedGrace {
				s.core.finish(Result{Status: runner.StatusDone, ExitStatus: -1})
				return
			}
		case st == StateStarting && time.Now().After(startBy):
			s.failToStart()
			return
		}
	}
}

// finishDead ends the Session from a dead pane's exit status.
func (s *tmuxSession) finishDead(status int) {
	res := Result{Status: runner.StatusDone, ExitStatus: status}
	if status != 0 {
		res.Status, res.Error = runner.StatusFailed, fmt.Sprintf("the Session exited with status %d", status)
	}
	if s.core.State() == StateStarting {
		res.Status, res.Error = runner.StatusFailed, fmt.Sprintf("failed to start: the pane exited with status %d before the first hook", status)
	}
	s.core.finish(res)
}

// failToStart ends a Session that never reported a SessionStart. Its process
// is stopped but its pane is kept, so the developer can read where it stuck
// (usually a folder-trust dialog, which no hook reports).
func (s *tmuxSession) failToStart() {
	if out, err := s.t.tmux("display-message", "-p", "-t", s.pane, "#{pane_pid}"); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(out)); err == nil && pid > 1 {
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	s.core.finish(Result{Status: runner.StatusFailed, ExitStatus: -1, Error: fmt.Sprintf("failed to start: no SessionStart hook within %s; the pane %s is kept", s.startWithin, s.pane)})
}

// cancel interrupts the Session, then kills its pane after the grace. At a
// permission prompt it types nothing: Enter at a prompt approves it, and the
// Works never answers one (ADR 0027).
func (s *tmuxSession) cancel() {
	if st := s.core.State(); st != StateWaitingOnPermission && st != StateEnded {
		s.t.tmux("send-keys", "-t", s.pane, "C-c")
	}
	deadline := time.Now().Add(s.t.dur(s.t.CancelGrace, DefaultCancelGrace))
	for time.Now().Before(deadline) {
		if dead, _, err := s.paneState(); err != nil || dead {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.tmux("kill-pane", "-t", s.pane) // already gone is fine
	s.core.finish(Result{Status: runner.StatusCancelled, ExitStatus: -1})
}

// paneState reads whether the pane's process has exited, and its status.
func (s *tmuxSession) paneState() (dead bool, status int, err error) {
	out, err := s.t.tmux("display-message", "-p", "-t", s.pane, "#{pane_dead} #{pane_dead_status}")
	if err != nil {
		return false, 0, err
	}
	d, st, _ := strings.Cut(out, " ")
	status, _ = strconv.Atoi(st)
	return d == "1", status, nil
}

// drainSpool delivers hook lines the receiver did not: the fallback for a hook
// whose post failed. A line already delivered over the socket is dropped by id.
func (s *tmuxSession) drainSpool() {
	f, err := os.Open(s.hookFile)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(s.spoolOff, io.SeekStart); err != nil {
		return
	}
	b, err := io.ReadAll(io.LimitReader(f, maxLine*4))
	if err != nil || len(b) == 0 {
		return
	}
	s.spoolOff += int64(len(b))
	b = append(s.spoolBuf, b...)
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		var d Delivery
		if json.Unmarshal(b[:i], &d) == nil {
			s.core.deliver(d)
		}
		b = b[i+1:]
	}
	s.spoolBuf = append([]byte(nil), b...) // a partial last line waits for the rest
}

// shellJoin renders argv as one shell command line.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// tmuxQuote quotes a path for a tmux config file.
func tmuxQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`).Replace(s) + `"`
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}

// newUUID is a random (version 4) UUID, for claude's --session-id.
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
