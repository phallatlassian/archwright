package works

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/store"
)

// DefaultProfiles are the developer's runner configurations the Guilds are
// bound against. Until the vendor adapters report their own, both runners
// stand in as fully capable and unrestricted; a Guild still only narrows them.
func DefaultProfiles() map[string]defs.Profile {
	caps := []string{defs.CapReadOnly, defs.CapToolDenylist, defs.CapIsolatedWorktree}
	return map[string]defs.Profile{"claude": {Capabilities: caps}, "codex": {Capabilities: caps}}
}

// UseDefinitions sets the loaded Guild and Jig catalogue.
func (r *Runs) UseDefinitions(reg *defs.Registry) {
	r.mu.Lock()
	r.defs = reg
	r.mu.Unlock()
}

// Definitions is the loaded catalogue, nil if none was set.
func (r *Runs) Definitions() *defs.Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.defs
}

// Catalogue is the Archwright's view of the roster: what Guilds and Jigs are
// loaded, and which definitions failed to load (the previous good one stays).
type Catalogue struct {
	Guilds []CatalogueGuild `json:"guilds"`
	Jigs   []defs.Jig       `json:"jigs"`
	Errors []defs.LoadError `json:"errors"`
}

// CatalogueGuild is a Guild as the Archwright chooses by: never its charter or skills.
type CatalogueGuild struct {
	Name     string      `json:"name"`
	Intake   string      `json:"intake"`
	Handback string      `json:"handback"`
	Access   defs.Access `json:"access"`
	Runners  []string    `json:"runners"`
	Source   string      `json:"source"`
}

func catalogueOf(reg *defs.Registry) Catalogue {
	c := Catalogue{Guilds: []CatalogueGuild{}, Jigs: reg.Jigs(), Errors: reg.Errors()}
	for _, g := range reg.Guilds() {
		c.Guilds = append(c.Guilds, CatalogueGuild{Name: g.Name, Intake: g.Intake, Handback: g.Handback, Access: g.Access, Runners: g.Runners, Source: g.Source})
	}
	return c
}

// ReloadDefinitions re-reads every definition and logs each failure.
func ReloadDefinitions(reg *defs.Registry) Catalogue {
	for _, e := range reg.Reload() {
		log.Printf("definition not loaded, previous kept: %v", e)
	}
	return catalogueOf(reg)
}

// instantiateJig starts a Plan for the Job from the named Jig: it needs no approval.
func instantiateJig(st *store.Store, reg *defs.Registry, jobID, jig string, params map[string]string) (store.Plan, error) {
	if reg == nil {
		return store.Plan{}, fmt.Errorf("%w: no definitions are loaded", errInvalidInput)
	}
	j, ok := reg.Jig(jig)
	if !ok {
		return store.Plan{}, fmt.Errorf("%w: no jig %q", errInvalidInput, jig)
	}
	steps, err := j.Instantiate(params)
	if err != nil {
		return store.Plan{}, fmt.Errorf("%w: %v", errInvalidInput, err)
	}
	return st.SetJigPlan(jobID, jig, steps)
}

func showCatalogue(c *callCtx, in json.RawMessage) (any, error) {
	if err := decode(in, &struct{}{}); err != nil {
		return nil, err
	}
	reg := c.runs.Definitions()
	if reg == nil {
		return nil, errors.New("no definitions are loaded")
	}
	return catalogueOf(reg), nil
}

func startJig(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Job    string            `json:"job"`
		Jig    string            `json:"jig"`
		Params map[string]string `json:"params"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if err := requireField("job", a.Job); err != nil {
		return nil, err
	}
	return instantiateJig(c.st, c.runs.Definitions(), a.Job, a.Jig, a.Params)
}

func init() {
	catalogue["show_catalogue"] = tool{roles: []string{RoleArchwright}, call: showCatalogue}
	catalogue["start_jig"] = tool{roles: []string{RoleArchwright}, call: startJig}
}
