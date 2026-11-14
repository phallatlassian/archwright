package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/store"
	"github.com/phallatlassian/archwright/internal/works"
)

// confirmed asks the developer a yes/no question at the terminal. It refuses
// outright when stdin is not one, so a script or agent cannot confirm for them.
func confirmed(stdin io.Reader, stdout io.Writer, what, question string) (bool, error) {
	if !isTerminal(stdin) {
		return false, fmt.Errorf("%s needs a developer at a terminal to confirm; run this yourself, not from a script or agent", what)
	}
	_, _ = fmt.Fprint(stdout, question+" [y/N] ")
	var answer string
	_, _ = fmt.Fscanln(stdin, &answer)
	return answer == "y" || answer == "Y" || answer == "yes", nil
}

// prune is `archwright prune [--dry-run] [--days N]`: remove the raw run
// content of Jobs that ended long enough ago (ADR 0017).
func prune(args []string, connect func() (*works.Client, error), emit func(any) error) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "show what would be pruned and change nothing")
	days := fs.Int("days", 0, "prune Jobs that ended more than this many days ago (default: retention_days in config.toml, else 30)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *days < 0 {
		return errors.New("--days must be positive")
	}
	c, err := connect()
	if err != nil {
		return err
	}
	var rep store.PruneReport
	if err := c.Do("POST", "/prune", map[string]any{"dry_run": *dry, "days": *days}, &rep); err != nil {
		return err
	}
	return emit(rep)
}

// jobPurgeOrDelete is `archwright job purge ID` and `job delete ID`; both
// confirm first, at a terminal.
func jobPurgeOrDelete(verb string, args []string, stdin io.Reader, stdout io.Writer, connect func() (*works.Client, error), emit func(any) error) error {
	if len(args) < 1 {
		return errors.New("missing ID\n" + usage)
	}
	id := args[0]
	if !isTerminal(stdin) {
		return fmt.Errorf("%sing a Job needs a developer at a terminal to confirm; run this yourself, not from a script or agent", verb)
	}
	c, err := connect()
	if err != nil {
		return err
	}
	var job store.Job
	if err := c.Do("GET", "/jobs/"+url.PathEscape(id), nil, &job); err != nil {
		return err
	}
	var q string
	switch verb {
	case "purge":
		q = fmt.Sprintf("Job %s %q: its raw run content is removed now and cannot be restored; its Plan, briefs, handbacks, findings, asks, gate results and usage are kept.\nPurge?", job.ID, job.Title)
	default:
		q = fmt.Sprintf("Job %s %q: the Job and everything held about it (Plan, briefs, handbacks, findings, asks, gate results, events) is removed and cannot be restored.", job.ID, job.Title)
		if job.Outcome == "" {
			q += " It is still open, so it is discarded first and its worktrees removed."
		}
		q += "\nDelete?"
	}
	ok, err := confirmed(stdin, stdout, verb+"ing a Job", q)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("not confirmed; nothing changed")
	}
	if verb == "purge" {
		var out any
		if err := c.Do("POST", "/jobs/"+url.PathEscape(id)+"/purge", nil, &out); err != nil {
			return err
		}
		return emit(out)
	}
	var out any
	if err := c.Do("DELETE", "/jobs/"+url.PathEscape(id), nil, &out); err != nil {
		return err
	}
	return emit(out)
}

// upgrade is `archwright upgrade`: restart the Works on this binary, which
// migrates the database after copying it aside (ADR 0016). It refuses while
// runs are active unless the developer confirms at a terminal.
func upgrade(dirs paths.Dirs, stdin io.Reader, stdout io.Writer, connect func() (*works.Client, error), emit func(any) error) error {
	c, running := works.Running(dirs)
	active := 0
	if running {
		var out struct{ Active int }
		if err := c.Do("GET", "/runs/active", nil, &out); err != nil {
			return err
		}
		active = out.Active
		if active > 0 {
			ok, err := confirmed(stdin, stdout, "upgrading while runs are active",
				fmt.Sprintf("%d run(s) are active; restarting the Works stops them, and each is re-dispatched from its stored brief when the Works starts again.\nRestart anyway?", active))
			if err != nil {
				return fmt.Errorf("refusing to restart the Works: %d run(s) are active: %w", active, err)
			}
			if !ok {
				return fmt.Errorf("refusing to restart the Works: %d run(s) are active and you did not confirm", active)
			}
		}
		if err := c.Do("POST", "/shutdown", nil, nil); err != nil {
			return err
		}
		if err := waitStopped(dirs); err != nil {
			return err
		}
	}
	c, err := connect()
	if err != nil {
		return err
	}
	var h works.Health
	if err := c.Do("GET", "/health", nil, &h); err != nil {
		return err
	}
	baks, _ := store.Backups(dirs.Database())
	return emit(map[string]any{
		"restarted":            running,
		"interrupted_runs":     active,
		"schema_version":       h.SchemaVersion,
		"pid":                  c.Info.PID,
		"pre_migration_copies": baks,
	})
}

func waitStopped(dirs paths.Dirs) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := works.Running(dirs); !ok {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("the Works did not stop; see " + dirs.Log())
}
