// Command archwright is the factory: the CLI and the Works daemon in one binary (ADR 0016).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
	"github.com/phallatlassian/archwright/internal/works"
)

const usage = `usage: archwright <command>

  site detect URL                          propose Site values and gate commands from a remote's base branch
  site register --url U --typecheck C --lint C --test C [--name N] [--base-branch B] [--cap N]
                                           register a remote repository as a Site (the trust act); the Works
                                           keeps its own clone; gates must be given. Local paths are refused
  site gates NAME [--typecheck C] [--lint C] [--test C]
                                           propose new gate commands (unset ones stay); takes effect only once approved
  site gate-changes [--status pending|approved|rejected]
                                           list proposed gate changes
  site gate-approve ID                     approve a proposed change (asks you to confirm at a terminal)
  site gate-reject ID                      reject a proposed change
  site list                                list Sites
  site remove NAME                         unregister a Site and delete its clone (Job history is kept)
  site resolve DIR                         show the Site whose remote is DIR's git origin; refused if unregistered
  job create --title T [--description D] [--site NAME | --path DIR]
                                           create a Job (on a registered Site; --path matches DIR's git origin)
  job close ID accepted|discarded          end a Job and remove its remaining worktrees
  worktree create ID integration|wright|readonly [--base-ref REF]
                                           create a Job's worktree from the Works' clone, after a fetch
  worktree list ID                         list a Job's worktrees
  worktree cleanup ID                      remove worktrees of Wrights merged into the integration branch
  job list                                 list Jobs
  job show ID                              show a Job and its Plan
  plan set ID [FILE]                       set a Job's Plan from JSON ({"steps":[...]}); stdin if no FILE
  plan show ID                             show a Job's Plan
  queue show                               show running and queued Wrights, the caps and any runner pause
  needs-you list                           list the Needs-you inbox (such as scope-overlap warnings)
  tool NAME [--id ID] [--wait D]           call a Works tool inside a run: JSON on stdin, JSON on stdout
                                           (exit 75 if the Works does not answer within D, default 60s)
  works status                             report whether the Works is running
  stop                                     stop the Works

All output is JSON. The Works starts on demand.`

// exitWorksUnreachable is the exit code of a tool call the Works never answered (EX_TEMPFAIL).
const exitWorksUnreachable = 75

// exitError carries a specific process exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "archwright:", err)
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "tool" {
		return tool(args[1:], stdin, stdout)
	}
	dirs, err := paths.Resolve(os.Getenv)
	if err != nil {
		return err
	}
	emit := func(v any) error {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	connect := func() (*works.Client, error) {
		return works.Connect(context.Background(), dirs, works.ExecSpawner)
	}
	call := func(method, path string, in any) error {
		c, err := connect()
		if err != nil {
			return err
		}
		var out json.RawMessage
		if err := c.Do(method, path, in, &out); err != nil {
			return err
		}
		return emit(out)
	}

	switch key(args, 2) {
	case "works serve":
		return serve(dirs)
	case "works status":
		c, ok := works.Running(dirs)
		if !ok {
			return emit(map[string]any{"running": false, "schema_version": store.SchemaVersion()})
		}
		return emit(map[string]any{"running": true, "pid": c.Info.PID, "port": c.Info.Port})
	case "job create":
		fs := flag.NewFlagSet("job create", flag.ContinueOnError)
		title := fs.String("title", "", "Job title")
		desc := fs.String("description", "", "Job description")
		site := fs.String("site", "", "registered Site name")
		dir := fs.String("path", "", "directory inside a registered Site")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		return call("POST", "/jobs", map[string]string{"title": *title, "description": *desc, "site": *site, "path": absPath(*dir)})
	case "site detect":
		p, err := arg(args, 2)
		if err != nil {
			return err
		}
		return call("GET", "/sites/detect?url="+url.QueryEscape(p), nil)
	case "site resolve":
		p, err := arg(args, 2)
		if err != nil {
			return err
		}
		return call("GET", "/sites/resolve?path="+url.QueryEscape(absPath(p)), nil)
	case "site remove":
		name, err := arg(args, 2)
		if err != nil {
			return err
		}
		return call("DELETE", "/sites/"+url.PathEscape(name), nil)
	case "site list":
		return call("GET", "/sites", nil)
	case "site register":
		fs := flag.NewFlagSet("site register", flag.ContinueOnError)
		var gates store.Gates
		in := sites.RegisterInput{}
		fs.StringVar(&in.Name, "name", "", "Site name (default: the repository name)")
		fs.StringVar(&in.URL, "url", "", "remote URL of the repository (https, ssh or user@host:path; not a local path)")
		fs.StringVar(&in.BaseBranch, "base-branch", "", "default branch the factory never commits to (default: the remote's default branch)")
		fs.IntVar(&in.ConcurrencyCap, "cap", 0, "concurrent runs (default 3)")
		fs.StringVar(&gates.Typecheck, "typecheck", "", "type-check gate command")
		fs.StringVar(&gates.Lint, "lint", "", "lint gate command")
		fs.StringVar(&gates.Test, "test", "", "test gate command")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if in.URL == "" {
			return errors.New("--url is required")
		}
		// Gates are the developer's confirmation: only sent when a gate flag
		// was given (an empty value means none); otherwise the Works refuses
		// and shows what it detected.
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "typecheck", "lint", "test":
				in.Gates = &gates
			}
		})
		return call("POST", "/sites", in)
	case "site gates":
		name, err := arg(args, 2)
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("site gates", flag.ContinueOnError)
		in := map[string]string{}
		for _, g := range []string{"typecheck", "lint", "test"} {
			fs.Func(g, g+" gate command (\"\" for none)", func(v string) error { in[g] = v; return nil })
		}
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		if len(in) == 0 {
			return errors.New("give at least one of --typecheck, --lint, --test")
		}
		return call("POST", "/sites/"+url.PathEscape(name)+"/gates", in)
	case "site gate-changes":
		fs := flag.NewFlagSet("site gate-changes", flag.ContinueOnError)
		status := fs.String("status", "", "only changes with this status")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		return call("GET", "/gate-changes?status="+url.QueryEscape(*status), nil)
	case "site gate-reject":
		id, err := arg(args, 2)
		if err != nil {
			return err
		}
		return call("POST", "/gate-changes/"+url.PathEscape(id)+"/reject", nil)
	case "site gate-approve":
		id, err := arg(args, 2)
		if err != nil {
			return err
		}
		if !isTerminal(stdin) {
			return errors.New("approving gate commands needs a developer at a terminal; run this yourself, not from a script or agent")
		}
		c, err := connect()
		if err != nil {
			return err
		}
		var pending []store.GateChange
		if err := c.Do("GET", "/gate-changes?status=pending", nil, &pending); err != nil {
			return err
		}
		for _, ch := range pending {
			if ch.ID != id {
				continue
			}
			_, _ = fmt.Fprintf(stdout, "Site %s will run these commands on every handback:\n  typecheck: %s\n  lint:      %s\n  test:      %s\nApprove? [y/N] ", ch.Site, ch.Gates.Typecheck, ch.Gates.Lint, ch.Gates.Test)
			var answer string
			_, _ = fmt.Fscanln(stdin, &answer)
			if answer != "y" && answer != "Y" && answer != "yes" {
				return errors.New("not approved")
			}
			var site json.RawMessage
			if err := c.Do("POST", "/gate-changes/"+url.PathEscape(id)+"/approve", nil, &site); err != nil {
				return err
			}
			return emit(site)
		}
		return fmt.Errorf("no pending gate change %q", id)
	case "worktree create":
		id, err := arg(args, 2)
		if err != nil {
			return err
		}
		kind, err := arg(args, 3)
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("worktree create", flag.ContinueOnError)
		ref := fs.String("base-ref", "", "base ref (default: the Job's integration branch, else the Site's base branch)")
		if err := fs.Parse(args[4:]); err != nil {
			return err
		}
		return call("POST", "/jobs/"+id+"/worktrees", map[string]string{"kind": kind, "base_ref": *ref})
	case "worktree list":
		id, err := arg(args, 2)
		if err != nil {
			return err
		}
		return call("GET", "/jobs/"+id+"/worktrees", nil)
	case "worktree cleanup":
		id, err := arg(args, 2)
		if err != nil {
			return err
		}
		return call("POST", "/jobs/"+id+"/worktrees/cleanup", nil)
	case "job close":
		id, err := arg(args, 2)
		if err != nil {
			return err
		}
		outcome, err := arg(args, 3)
		if err != nil {
			return err
		}
		return call("POST", "/jobs/"+id+"/close", map[string]string{"outcome": outcome})
	case "queue show":
		return call("GET", "/queue", nil)
	case "needs-you list":
		return call("GET", "/needs-you", nil)
	case "job list":
		return call("GET", "/jobs", nil)
	case "job show":
		id, err := arg(args, 2)
		if err != nil {
			return err
		}
		return call("GET", "/jobs/"+id, nil)
	case "plan show":
		id, err := arg(args, 2)
		if err != nil {
			return err
		}
		return call("GET", "/jobs/"+id+"/plan", nil)
	case "plan set":
		id, err := arg(args, 2)
		if err != nil {
			return err
		}
		in := stdin
		if len(args) > 3 {
			f, err := os.Open(args[3])
			if err != nil {
				return err
			}
			defer f.Close()
			in = f
		}
		var plan json.RawMessage
		if err := json.NewDecoder(in).Decode(&plan); err != nil {
			return fmt.Errorf("reading plan JSON: %w", err)
		}
		return call("PUT", "/jobs/"+id+"/plan", plan)
	}
	if args[0] == "stop" {
		c, ok := works.Running(dirs)
		if !ok {
			return emit(map[string]bool{"stopped": false})
		}
		if err := c.Do("POST", "/shutdown", nil, nil); err != nil {
			return err
		}
		return emit(map[string]bool{"stopped": true})
	}
	return errors.New(usage)
}

// isTerminal reports whether r is an interactive terminal.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0 && fi.Mode()&os.ModeDevice != 0
}

// absPath makes a CLI path absolute against the working directory; "" stays "".
func absPath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func key(args []string, n int) string {
	if len(args) < n {
		return ""
	}
	return args[0] + " " + args[1]
}

func arg(args []string, i int) (string, error) {
	if len(args) <= i {
		return "", errors.New("missing ID\n" + usage)
	}
	return args[i], nil
}

func serve(dirs paths.Dirs) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := works.Serve(ctx, dirs, nil)
	if errors.Is(err, works.ErrAlreadyRunning) {
		return nil // a second start reuses the running instance
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s works: %v\n", time.Now().Format(time.RFC3339), err)
	}
	return err
}

// tool makes one call to the Works over the run's spool (ADR 0014, 0018).
func tool(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("missing tool name\n" + usage)
	}
	name := args[0]
	fs := flag.NewFlagSet("tool", flag.ContinueOnError)
	id := fs.String("id", spool.NewID(), "request id; repeating one is idempotent")
	wait := fs.Duration("wait", spool.DefaultWait, "how long to wait for the Works")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	env, err := spool.EnvFrom(os.Getenv)
	if err != nil {
		return err
	}
	input, err := io.ReadAll(io.LimitReader(stdin, spool.MaxRequestBytes))
	if err != nil {
		return err
	}
	input = bytes.TrimSpace(input)
	if len(input) == 0 {
		input = nil
	} else if !json.Valid(input) {
		return errors.New("tool input is not valid JSON")
	}
	resp, err := spool.Call(context.Background(), env, *id, name, input, *wait)
	if errors.Is(err, spool.ErrTimeout) {
		return &exitError{exitWorksUnreachable, err}
	}
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if !resp.OK {
		enc.Encode(resp)
		return resp.Error
	}
	return enc.Encode(json.RawMessage(resp.Output))
}
