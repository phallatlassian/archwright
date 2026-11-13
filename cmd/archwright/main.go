// Command archwright is the factory: the CLI and the Works daemon in one binary (ADR 0016).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
	"github.com/phallatlassian/archwright/internal/works"
)

const usage = `usage: archwright <command>

  job create --title T [--description D]   create a Job
  job list                                 list Jobs
  job show ID                              show a Job and its Plan
  plan set ID [FILE]                       set a Job's Plan from JSON ({"steps":[...]}); stdin if no FILE
  plan show ID                             show a Job's Plan
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
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		return call("POST", "/jobs", map[string]string{"title": *title, "description": *desc})
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
	if len(strings.TrimSpace(string(input))) > 0 && !json.Valid(input) {
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
