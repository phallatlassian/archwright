// Package defs loads the factory's Guild and Jig definitions: embedded
// read-only defaults plus the developer's config directory, where the same
// name replaces the shipped definition entirely (ADR 0009). Loading fails
// closed per definition, and a Guild can only narrow permissions (ADR 0011).
package defs

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"sort"
	"sync"
)

// Access is what a Guild's runs may do to a Site.
type Access string

const (
	ReadOnly  Access = "read-only"
	ReadWrite Access = "read-write"
)

// Guild is a loaded Guild definition: GUILD.md, warden/SKILL.md and wrights/*.md.
type Guild struct {
	Name         string            `json:"name"`
	Intake       string            `json:"intake"`
	Handback     string            `json:"handback"`
	Runner       string            `json:"runner"`  // the default runner
	Runners      []string          `json:"runners"` // the permitted set
	Requires     []string          `json:"requires,omitempty"`
	Access       Access            `json:"access"`
	DeniedTools  []string          `json:"denied_tools,omitempty"`
	ReworkRounds int               `json:"rework_rounds,omitempty"`
	Skills       []string          `json:"skills,omitempty"`
	Charter      string            `json:"charter"`
	Warden       string            `json:"warden"`  // warden/SKILL.md
	Wrights      map[string]string `json:"wrights"` // brief templates by kind
	Source       string            `json:"source"`  // shipped or config
}

// Param is one parameter of a Jig.
type Param struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description"`
	Required    bool   `json:"required,omitempty" yaml:"required"`
	Default     string `json:"default,omitempty" yaml:"default"`
}

// JigStep is one step of a Jig.
type JigStep struct {
	ID         string   `json:"id" yaml:"id"`
	Guild      string   `json:"guild" yaml:"guild"`
	Task       string   `json:"task" yaml:"task"`
	Inputs     []string `json:"inputs,omitempty" yaml:"inputs"`
	Outputs    []string `json:"outputs,omitempty" yaml:"outputs"`
	BlockedBy  []string `json:"blocked_by,omitempty" yaml:"blocked_by"`
	Checkpoint bool     `json:"checkpoint,omitempty" yaml:"checkpoint"`
}

// Jig is a loaded Jig definition: a prebuilt Plan.
type Jig struct {
	Name        string    `json:"name" yaml:"name"`
	Description string    `json:"description" yaml:"description"`
	Parameters  []Param   `json:"parameters,omitempty" yaml:"parameters"`
	Steps       []JigStep `json:"steps" yaml:"steps"`
	Source      string    `json:"source" yaml:"-"`
}

// LoadError is one definition that failed to load. The previous good
// definition of that name, if any, stays in force.
type LoadError struct {
	Kind   string `json:"kind"` // guild or jig
	Name   string `json:"name"`
	Source string `json:"source"` // the path read
	Err    error  `json:"-"`
	Reason string `json:"error"`
}

func (e LoadError) Error() string {
	return fmt.Sprintf("%s %q (%s): %v", e.Kind, e.Name, e.Source, e.Err)
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Registry holds the loaded catalogue.
type Registry struct {
	shipped fs.FS
	config  string

	mu     sync.RWMutex
	guilds map[string]Guild
	jigs   map[string]Jig
	errs   []LoadError
}

// New returns an empty Registry over the shipped defaults and the config
// directory (which may be empty or absent). Call Reload to fill it.
func New(shipped fs.FS, configDir string) *Registry {
	return &Registry{shipped: shipped, config: configDir, guilds: map[string]Guild{}, jigs: map[string]Jig{}}
}

type attempt[T any] struct {
	def    T
	err    error
	source string
}

// Reload re-reads every definition and returns the definitions that failed.
// An invalid definition is not loaded: the previous good one of that name
// (else the shipped one) stays.
func (r *Registry) Reload() []LoadError {
	var cfg fs.FS
	if r.config != "" {
		cfg = os.DirFS(r.config)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []LoadError
	guilds := map[string]Guild{}
	shippedG, configG := readGuilds(r.shipped, "shipped"), readGuilds(cfg, "config")
	for _, name := range union(shippedG, configG) {
		if c, ok := configG[name]; ok && c.err == nil {
			guilds[name] = c.def
			continue
		} else if ok {
			errs = append(errs, LoadError{Kind: "guild", Name: name, Source: c.source, Err: c.err})
			if prev, ok := r.guilds[name]; ok {
				guilds[name] = prev
				continue
			}
		}
		if s, ok := shippedG[name]; ok {
			if s.err != nil {
				errs = append(errs, LoadError{Kind: "guild", Name: name, Source: s.source, Err: s.err})
				continue
			}
			guilds[name] = s.def
		}
	}

	jigs := map[string]Jig{}
	shippedJ, configJ := readJigs(r.shipped, "shipped"), readJigs(cfg, "config")
	for _, name := range union(shippedJ, configJ) {
		valid := func(a attempt[Jig]) error {
			if a.err != nil {
				return a.err
			}
			return a.def.validate(guilds)
		}
		c, hasConfig := configJ[name]
		if hasConfig {
			if err := valid(c); err == nil {
				jigs[name] = c.def
				continue
			} else {
				errs = append(errs, LoadError{Kind: "jig", Name: name, Source: c.source, Err: err})
			}
			if prev, ok := r.jigs[name]; ok && prev.validate(guilds) == nil {
				jigs[name] = prev
				continue
			}
		}
		if s, ok := shippedJ[name]; ok {
			if err := valid(s); err != nil {
				errs = append(errs, LoadError{Kind: "jig", Name: name, Source: s.source, Err: err})
				continue
			}
			jigs[name] = s.def
		}
	}

	sort.Slice(errs, func(i, j int) bool {
		if errs[i].Kind != errs[j].Kind {
			return errs[i].Kind < errs[j].Kind
		}
		return errs[i].Name < errs[j].Name
	})
	for i := range errs {
		errs[i].Reason = errs[i].Err.Error()
	}
	r.guilds, r.jigs, r.errs = guilds, jigs, errs
	return append([]LoadError(nil), errs...)
}

func union[T any](a, b map[string]T) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range []map[string]T{a, b} {
		for n := range m {
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	return names
}

// Errors are the definitions that failed in the last Reload.
func (r *Registry) Errors() []LoadError {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]LoadError{}, r.errs...)
}

// Guild looks a loaded Guild up by name.
func (r *Registry) Guild(name string) (Guild, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.guilds[name]
	g.Wrights = maps.Clone(g.Wrights) // callers cannot change the registry's copy
	return g, ok
}

// Guilds lists the loaded Guilds by name.
func (r *Registry) Guilds() []Guild {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Guild, 0, len(r.guilds))
	for _, n := range union(r.guilds, map[string]Guild{}) {
		out = append(out, r.guilds[n])
	}
	return out
}

// Jig looks a loaded Jig up by name.
func (r *Registry) Jig(name string) (Jig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jigs[name]
	return j, ok
}

// Jigs lists the loaded Jigs by name.
func (r *Registry) Jigs() []Jig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Jig, 0, len(r.jigs))
	for _, n := range union(r.jigs, map[string]Jig{}) {
		out = append(out, r.jigs[n])
	}
	return out
}
