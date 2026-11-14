package works

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/phallatlassian/archwright/internal/store"
)

// Roles an agent run can have. The tool catalogue is per role (ADR 0014).
const (
	RoleArchwright = "archwright"
	RoleWarden     = "warden"
	RoleWright     = "wright"
)

// errInvalidInput marks a request whose input fails the tool's schema.
var errInvalidInput = errors.New("invalid input")

// callCtx is what a tool call can reach: the store, and the run making the call.
type callCtx struct {
	st   *store.Store
	runs *Runs
	run  *run
}

type tool struct {
	roles []string
	call  func(c *callCtx, in json.RawMessage) (any, error)
}

func (t tool) allows(role string) bool {
	for _, r := range t.roles {
		if r == role {
			return true
		}
	}
	return false
}

// decode is the schema check: unknown fields and trailing data are rejected.
func decode(in json.RawMessage, v any) error {
	if len(bytes.TrimSpace(in)) == 0 {
		in = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", errInvalidInput, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data after input", errInvalidInput)
	}
	return nil
}

func requireField(name, v string) error {
	if v == "" {
		return fmt.Errorf("%w: %s is required", errInvalidInput, name)
	}
	return nil
}

// catalogue is the tool API served over the spool, keyed by tool name.
var catalogue = map[string]tool{
	"create_job": {roles: []string{RoleArchwright}, call: func(c *callCtx, in json.RawMessage) (any, error) {
		var a struct{ Title, Description, Site string }
		if err := decode(in, &a); err != nil {
			return nil, err
		}
		if a.Site != "" { // the Site the launch hint named (ADR 0010)
			return c.st.CreateJobOn(a.Site, a.Title, a.Description)
		}
		return c.st.CreateJob(a.Title, a.Description)
	}},
	"list_jobs": {roles: []string{RoleArchwright}, call: func(c *callCtx, in json.RawMessage) (any, error) {
		if err := decode(in, &struct{}{}); err != nil {
			return nil, err
		}
		return c.st.ListJobs()
	}},
	"show_job": {roles: []string{RoleArchwright}, call: func(c *callCtx, in json.RawMessage) (any, error) {
		var a struct{ ID string }
		if err := decode(in, &a); err != nil {
			return nil, err
		}
		if err := requireField("id", a.ID); err != nil {
			return nil, err
		}
		return jobWithPlan(c.st, a.ID)
	}},
	"set_plan": {roles: []string{RoleArchwright}, call: func(c *callCtx, in json.RawMessage) (any, error) {
		var a struct {
			JobID string       `json:"job_id"`
			Steps []store.Step `json:"steps"`
		}
		if err := decode(in, &a); err != nil {
			return nil, err
		}
		if err := requireField("job_id", a.JobID); err != nil {
			return nil, err
		}
		return c.st.SetPlan(a.JobID, a.Steps)
	}},
	"show_plan": {roles: []string{RoleArchwright}, call: func(c *callCtx, in json.RawMessage) (any, error) {
		var a struct {
			JobID string `json:"job_id"`
		}
		if err := decode(in, &a); err != nil {
			return nil, err
		}
		if err := requireField("job_id", a.JobID); err != nil {
			return nil, err
		}
		return c.st.GetPlan(a.JobID)
	}},
}

// jobWithPlan reads a Job and its Plan, if it has one.
func jobWithPlan(st *store.Store, id string) (JobWithPlan, error) {
	j, err := st.GetJob(id)
	if err != nil {
		return JobWithPlan{}, err
	}
	out := JobWithPlan{Job: j, Asks: []store.Ask{}}
	for _, status := range []string{store.AskOpen, store.AskEscalated} {
		asks, err := st.Asks(store.AskFilter{Job: id, Status: status})
		if err != nil {
			return JobWithPlan{}, err
		}
		out.Asks = append(out.Asks, asks...)
	}
	for _, a := range out.Asks {
		out.BlockedOnYou = out.BlockedOnYou || (a.Holder == store.HolderArchwright && a.Status == store.AskOpen && a.Blocking)
	}
	if p, err := st.GetPlan(id); err == nil {
		out.Plan = &p
	} else if !errors.Is(err, store.ErrNotFound) {
		return JobWithPlan{}, err
	}
	return out, nil
}
