package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/phallatlassian/archwright/internal/askpage"
	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
	"github.com/phallatlassian/archwright/internal/works"
)

// askCommand serves `archwright ask ...` (ADR 0015). The developer's own
// actions (answer, accept-default, skip, cancel) go to the Works as the
// dashboard would; `page`, `end` and `record` are how the Archwright puts an
// ask to the developer as a Lavish page and records the answer through the
// tool API, falling back to plain text without lavish-axi.
func askCommand(args []string, stdin io.Reader, stdout io.Writer, dirs paths.Dirs, connect func() (*works.Client, error)) error {
	emit := func(v any) error {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
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
	fetch := func(id string) (store.Ask, store.Job, error) {
		var a store.Ask
		var j store.Job
		c, err := connect()
		if err != nil {
			return a, j, err
		}
		if err := c.Do("GET", "/asks/"+url.PathEscape(id), nil, &a); err != nil {
			return a, j, err
		}
		return a, j, c.Do("GET", "/jobs/"+url.PathEscape(a.JobID), nil, &j)
	}
	pageDir := filepath.Join(dirs.State, "asks")
	sub := args[1]
	if sub == "list" {
		fs := flag.NewFlagSet("ask list", flag.ContinueOnError)
		status := fs.String("status", "", "only asks with this status (open, escalated, answered, cancelled)")
		holder := fs.String("holder", "", "only asks held by warden or archwright")
		job := fs.String("job", "", "only this Job's asks")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		q := url.Values{"status": {*status}, "holder": {*holder}, "job": {*job}}
		return call("GET", "/asks?"+q.Encode(), nil)
	}
	id, err := arg(args, 2)
	if err != nil {
		return err
	}
	switch sub {
	case "show":
		a, j, err := fetch(id)
		if err != nil {
			return err
		}
		return emit(map[string]any{"ask": a, "text": askpage.Text(a, j)})
	case "page":
		a, j, err := fetch(id)
		if err != nil {
			return err
		}
		if a.Status != store.AskOpen {
			return fmt.Errorf("ask %s is already %s; nothing to put to the developer", a.ID, a.Status)
		}
		p, err := askpage.Present(a, j, pageDir, askpage.ExecLavish())
		if err != nil {
			return err
		}
		return emit(p)
	case "end":
		a, _, err := fetch(id)
		if err != nil {
			return err
		}
		if err := askpage.End(a, pageDir, askpage.ExecLavish()); err != nil {
			return err
		}
		return emit(map[string]any{"ask_id": a.ID, "page_ended": true})
	case "record":
		return recordSubmission(id, stdin, emit, pageDir)
	case "answer":
		var in struct {
			Answers []store.Answer `json:"answers"`
		}
		if err := json.NewDecoder(stdin).Decode(&in); err != nil {
			return fmt.Errorf("reading answers JSON ({\"answers\":[...]}): %w", err)
		}
		return call("POST", "/asks/"+url.PathEscape(id)+"/answer", in)
	case "accept-default", "skip", "cancel":
		return call("POST", "/asks/"+url.PathEscape(id)+"/"+sub, nil)
	}
	return errors.New(usage)
}

// recordSubmission records what the developer submitted on an ask's Lavish
// page (the prompt data read with `lavish-axi poll`) through the tool API. If
// the Works rejects it because another surface answered first, nothing is
// recorded and the page is ended either way.
func recordSubmission(id string, stdin io.Reader, emit func(any) error, pageDir string) error {
	env, err := spool.EnvFrom(os.Getenv)
	if err != nil {
		return err
	}
	callTool := func(name string, in json.RawMessage) (spool.Response, error) {
		resp, err := spool.Call(context.Background(), env, spool.NewID(), name, in, spool.DefaultWait)
		if errors.Is(err, spool.ErrTimeout) {
			return resp, &exitError{exitWorksUnreachable, err}
		}
		return resp, err
	}
	shown, err := callTool("show_ask", mustJSON(map[string]string{"ask_id": id}))
	if err != nil {
		return err
	}
	if !shown.OK {
		return shown.Error
	}
	var a store.Ask
	if err := json.Unmarshal(shown.Output, &a); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, spool.MaxRequestBytes))
	if err != nil {
		return err
	}
	s, err := askpage.ParseSubmission(raw, a)
	if err != nil {
		return err // the page stays open for the developer to correct
	}
	name, in := s.Tool()
	resp, err := callTool(name, in)
	if err != nil {
		return err
	}
	endErr := askpage.End(a, pageDir, askpage.ExecLavish())
	ended := endErr == nil
	switch {
	case resp.OK:
		return emit(map[string]any{"recorded": true, "ask_id": id, "tool": name, "result": json.RawMessage(resp.Output), "page_ended": ended})
	case resp.Error.Code == spool.CodeConflict:
		return emit(map[string]any{"recorded": false, "ask_id": id, "rejected": resp.Error.Message, "page_ended": ended,
			"tell_developer": "This ask was already settled elsewhere (the first answer wins), so your submission was not recorded."})
	}
	return resp.Error
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
