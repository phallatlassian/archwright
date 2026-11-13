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
	"syscall"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/store"
	"github.com/phallatlassian/archwright/internal/works"
)

const usage = `usage: archwright <command>

  job create --title T [--description D]   create a Job
  job list                                 list Jobs
  job show ID                              show a Job and its Plan
  plan set ID [FILE]                       set a Job's Plan from JSON ({"steps":[...]}); stdin if no FILE
  plan show ID                             show a Job's Plan
  works status                             report whether the Works is running
  stop                                     stop the Works

All output is JSON. The Works starts on demand.`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "archwright:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
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
