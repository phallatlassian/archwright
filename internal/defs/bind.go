package defs

import (
	"errors"
	"fmt"
	"sort"
)

// ErrCapability means a Guild's requirements cannot be met by the runner.
var ErrCapability = errors.New("capability not met")

// Profile is the developer's own configuration of one runner, as its adapter
// reports it: what it can enforce, and what the developer's rules and the
// managed settings already restrict. A Guild can only narrow it.
type Profile struct {
	Capabilities []string
	Access       Access   // ReadOnly if the developer's runner is read-only; empty means read-write
	DeniedTools  []string // denied by the developer's rules or managed policy
}

// Binding is a Guild bound to a runner: the permissions its runs get.
type Binding struct {
	Guild            string   `json:"guild"`
	Runner           string   `json:"runner"`
	Access           Access   `json:"access"`
	DeniedTools      []string `json:"denied_tools,omitempty"`
	DetachedWorktree bool     `json:"detached_worktree,omitempty"` // read-only Guilds work in detached worktrees
}

// Bind binds g to runner (the Guild's default if empty). The result is never
// wider than the profile: read-write narrows to read-only on a read-only
// profile, and denied tools are the union. A requirement the runner cannot
// enforce fails the bind.
func Bind(g Guild, runner string, profiles map[string]Profile) (Binding, error) {
	if runner == "" {
		runner = g.Runner
	}
	if !contains(g.Runners, runner) {
		return Binding{}, fmt.Errorf("guild %q does not permit runner %q (permitted: %v)", g.Name, runner, g.Runners)
	}
	p, ok := profiles[runner]
	if !ok {
		return Binding{}, fmt.Errorf("guild %q: runner %q is not available: %w", g.Name, runner, ErrCapability)
	}
	need := append([]string(nil), g.Requires...)
	if g.Access == ReadOnly {
		need = append(need, CapReadOnly)
	}
	if len(g.DeniedTools) > 0 {
		need = append(need, CapToolDenylist)
	}
	for _, c := range need {
		if !contains(p.Capabilities, c) {
			return Binding{}, fmt.Errorf("guild %q needs %s, which runner %q cannot enforce: %w", g.Name, c, runner, ErrCapability)
		}
	}
	b := Binding{Guild: g.Name, Runner: runner, Access: g.Access}
	if p.Access == ReadOnly {
		b.Access = ReadOnly
	}
	b.DetachedWorktree = b.Access == ReadOnly
	seen := map[string]bool{}
	for _, t := range append(append([]string(nil), p.DeniedTools...), g.DeniedTools...) {
		if !seen[t] {
			seen[t] = true
			b.DeniedTools = append(b.DeniedTools, t)
		}
	}
	sort.Strings(b.DeniedTools)
	return b, nil
}
