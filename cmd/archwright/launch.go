package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/phallatlassian/archwright/internal/config"
	"github.com/phallatlassian/archwright/internal/launch"
	"github.com/phallatlassian/archwright/internal/paths"
	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
	"github.com/phallatlassian/archwright/internal/works"
)

// hookWait bounds how long a hook waits for the Works, so a dead Works never stalls a turn.
const hookWait = 10 * time.Second

// launchArchwright opens the Archwright (ADR 0001). A Works that hosts Sessions
// spawns and owns it in the `archwright` window of its tmux session, and this
// attaches the terminal to that window (ADR 0026). A Works that does not runs
// the developer's own claude or codex here, as an interactive session of this
// terminal: that is the Works running the scripted fakes, which have no Session
// to host it in.
func launchArchwright(dirs paths.Dirs, connect func() (*works.Client, error)) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	c, err := connect()
	if err != nil {
		return err
	}
	// The launch directory is only a hint: not a registered Site is fine.
	site := ""
	var found store.Site
	if err := c.Do("GET", "/sites/resolve?path="+url.QueryEscape(cwd), nil, &found); err == nil {
		site = found.Name
	}
	var info works.ArchwrightInfo
	// The Works runs the doctor preflight before it starts any run, which can
	// take a while on a cold start: wait for it and say so rather than hang.
	note := time.AfterFunc(2*time.Second, func() {
		fmt.Fprintln(os.Stderr, "archwright: running doctor before the first run (checking the installed runners), this can take a minute...")
	})
	err = c.DoSlow("POST", "/archwright", map[string]string{"Site": site}, &info)
	note.Stop()
	switch {
	case err == nil:
		return attachArchwright(os.Getenv)
	case err.Error() != works.MsgArchwrightNotHosted:
		return err
	}
	return runArchwrightHere(dirs, c, site)
}

// attachArchwright attaches this terminal to the Archwright's window in the
// Works' own tmux server. The Works owns the Session: leaving the window, or
// ending this command, does not end it.
func attachArchwright(getenv func(string) string) error {
	bin := getenv(works.EnvTmuxBinary)
	if bin == "" {
		bin = "tmux"
	}
	cmd := exec.Command(bin, "-L", hosted.DefaultSocketName, "attach-session", "-t", "="+hosted.DefaultSessionName+":archwright")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "TMUX=") // the Works' server is not the developer's own, which may be the one this runs in
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &exitError{ee.ExitCode(), fmt.Errorf("tmux exited: %w", err)}
		}
		return fmt.Errorf("attaching to the Archwright (tmux -L %s attach -t %s:archwright): %w", hosted.DefaultSocketName, hosted.DefaultSessionName, err)
	}
	return nil
}

// runArchwrightHere starts the interactive Archwright session in this terminal
// (ADR 0001) and returns once it ends. It runs the developer's own claude or
// codex, under their own permissions, in the neutral factory home.
func runArchwrightHere(dirs paths.Dirs, c *works.Client, site string) error {
	choice, err := config.Runner(dirs)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(choice)
	if err != nil {
		return fmt.Errorf("the %s runner is chosen for this install but %q is not on PATH", choice, choice)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	home := launch.Home(dirs)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	var env works.RunEnv
	note := time.AfterFunc(2*time.Second, func() {
		fmt.Fprintln(os.Stderr, "archwright: running doctor before the first run (checking the installed runners), this can take a minute...")
	})
	err = c.DoSlow("POST", "/runs", map[string]string{"Role": works.RoleArchwright, "SpoolRoot": launch.SpoolRoot(choice, os.Getuid(), os.TempDir())}, &env)
	note.Stop()
	if err != nil {
		return err
	}
	defer c.Do("DELETE", "/runs/"+url.PathEscape(env.Run), nil, nil)

	cmd, err := launch.Build(launch.Input{Runner: choice, Exe: exe, Home: home, Site: site, Run: spool.Env{Run: env.Run, Dir: env.Spool, Token: env.Token}})
	if err != nil {
		return err
	}
	p := exec.Command(bin, cmd.Args...)
	p.Dir = cmd.Dir
	p.Env = append(os.Environ(), cmd.Env...)
	p.Stdin, p.Stdout, p.Stderr = os.Stdin, os.Stdout, os.Stderr
	// The runner shares the terminal and gets its own Ctrl-C; this process
	// waits it out and then ends the run.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	if err := p.Start(); err != nil {
		return err
	}
	go func() {
		for s := range sig {
			if s == syscall.SIGTERM {
				_ = p.Process.Signal(s)
			}
		}
	}()
	err = p.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &exitError{ee.ExitCode(), fmt.Errorf("%s exited: %w", choice, err)}
	}
	return err
}

// hook answers one of the Archwright's runner hooks over the run's spool.
func hook(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: archwright hook session-start|prompt|post-tool|stop")
	}
	env, err := spool.EnvFrom(os.Getenv)
	if err != nil {
		return err
	}
	// PostToolUse runs after every tool call: it asks the Works only when the
	// Works marked the inbox as holding unread items.
	if args[0] == "post-tool" && !launch.HasUnread(env.Dir) {
		return nil
	}
	call := func(tool string, input json.RawMessage) (json.RawMessage, error) {
		resp, err := spool.Call(context.Background(), env, spool.NewID(), tool, input, hookWait)
		if err != nil {
			return nil, err
		}
		if !resp.OK {
			return nil, resp.Error
		}
		return resp.Output, nil
	}
	return launch.HookEnv(args[0], call, os.Getenv, stdout)
}
