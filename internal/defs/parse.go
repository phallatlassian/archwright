package defs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/phallatlassian/archwright/internal/store"
)

// Capabilities a runner adapter may offer; a Guild's access and denied tools
// need the matching ones, and a restriction the runner cannot enforce fails
// the bind (ADR 0009).
const (
	CapReadOnly         = "enforce_read_only"
	CapToolDenylist     = "deny_tools"
	CapIsolatedWorktree = "isolated_worktree"
)

var knownCaps = map[string]bool{CapReadOnly: true, CapToolDenylist: true, CapIsolatedWorktree: true}
var knownRunners = map[string]bool{"claude": true, "codex": true}

// manifest is GUILD.md's frontmatter. Unknown keys are rejected, so no key can
// widen what the developer's runner configuration allows.
type manifest struct {
	Name         string   `yaml:"name"`
	Intake       string   `yaml:"intake"`
	Handback     string   `yaml:"handback"`
	Runner       string   `yaml:"runner"`
	Runners      []string `yaml:"runners"`
	Requires     []string `yaml:"requires"`
	Access       Access   `yaml:"access"`
	DeniedTools  []string `yaml:"denied_tools"`
	ReworkRounds int      `yaml:"rework_rounds"`
	Skills       []string `yaml:"skills"`
}

func readGuilds(fsys fs.FS, source string) map[string]attempt[Guild] {
	out := map[string]attempt[Guild]{}
	if fsys == nil {
		return out
	}
	entries, err := fs.ReadDir(fsys, "guilds")
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !e.IsDir() {
			continue
		}
		g, err := loadGuild(fsys, path.Join("guilds", name), name, source)
		out[name] = attempt[Guild]{def: g, err: err, source: path.Join(source, "guilds", name)}
	}
	return out
}

func loadGuild(fsys fs.FS, dir, name, source string) (Guild, error) {
	if !nameRE.MatchString(name) {
		return Guild{}, fmt.Errorf("guild name must be lower-case letters, digits and hyphens")
	}
	raw, err := fs.ReadFile(fsys, path.Join(dir, "GUILD.md"))
	if err != nil {
		return Guild{}, fmt.Errorf("reading GUILD.md: %w", err)
	}
	front, charter, err := splitFrontmatter(raw)
	if err != nil {
		return Guild{}, err
	}
	var m manifest
	if err := decodeStrict(front, &m); err != nil {
		return Guild{}, fmt.Errorf("GUILD.md manifest: %w", err)
	}
	g := Guild{Name: name, Intake: m.Intake, Handback: m.Handback, Runner: m.Runner, Runners: m.Runners, Requires: m.Requires,
		Access: m.Access, DeniedTools: m.DeniedTools, ReworkRounds: m.ReworkRounds, Skills: m.Skills,
		Charter: strings.TrimSpace(string(charter)), Wrights: map[string]string{}, Source: source}
	if g.Access == "" {
		g.Access = ReadOnly // the narrowest, when the manifest does not say
	}
	if m.Name != "" && m.Name != name {
		return Guild{}, fmt.Errorf("manifest name %q does not match its directory %q", m.Name, name)
	}
	if err := g.validate(); err != nil {
		return Guild{}, err
	}
	w, err := fs.ReadFile(fsys, path.Join(dir, "warden", "SKILL.md"))
	if err != nil {
		return Guild{}, fmt.Errorf("reading warden/SKILL.md: %w", err)
	}
	if strings.TrimSpace(string(w)) == "" {
		return Guild{}, errors.New("warden/SKILL.md is empty")
	}
	g.Warden = string(w)
	if files, err := fs.ReadDir(fsys, path.Join(dir, "wrights")); err == nil {
		for _, f := range files {
			kind, ok := strings.CutSuffix(f.Name(), ".md")
			if !ok || f.IsDir() {
				continue
			}
			b, err := fs.ReadFile(fsys, path.Join(dir, "wrights", f.Name()))
			if err != nil {
				return Guild{}, err
			}
			g.Wrights[kind] = string(b)
		}
	}
	return g, nil
}

func (g Guild) validate() error {
	if strings.TrimSpace(g.Intake) == "" || strings.TrimSpace(g.Handback) == "" {
		return errors.New("manifest needs intake and handback")
	}
	if g.Charter == "" {
		return errors.New("GUILD.md has no charter body")
	}
	if g.Access != ReadOnly && g.Access != ReadWrite {
		return fmt.Errorf("access %q must be %s or %s", g.Access, ReadOnly, ReadWrite)
	}
	if len(g.Runners) == 0 {
		return errors.New("manifest needs a permitted runners set")
	}
	for _, r := range g.Runners {
		if !knownRunners[r] {
			return fmt.Errorf("unknown runner %q", r)
		}
	}
	if !contains(g.Runners, g.Runner) {
		return fmt.Errorf("default runner %q is not in the permitted set %v", g.Runner, g.Runners)
	}
	for _, c := range g.Requires {
		if !knownCaps[c] {
			return fmt.Errorf("unknown capability %q", c)
		}
	}
	if g.ReworkRounds < 0 {
		return errors.New("rework_rounds cannot be negative")
	}
	return nil
}

func splitFrontmatter(b []byte) (front, body []byte, err error) {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(b, []byte("---\n"))
	if !ok {
		return nil, nil, errors.New("GUILD.md must start with a --- frontmatter manifest")
	}
	if i := bytes.Index(rest, []byte("\n---\n")); i >= 0 {
		return rest[:i+1], rest[i+5:], nil
	}
	if front, ok := bytes.CutSuffix(rest, []byte("\n---")); ok {
		return append(front, '\n'), nil, nil
	}
	return nil, nil, errors.New("GUILD.md frontmatter is not closed with ---")
}

func decodeStrict(b []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("more than one YAML document")
	}
	return nil
}

func readJigs(fsys fs.FS, source string) map[string]attempt[Jig] {
	out := map[string]attempt[Jig]{}
	if fsys == nil {
		return out
	}
	entries, _ := fs.ReadDir(fsys, "jigs")
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".yaml")
		if !ok {
			continue
		}
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		file := path.Join("jigs", e.Name())
		j, err := loadJig(fsys, file, stem, source)
		out[stem] = attempt[Jig]{def: j, err: err, source: path.Join(source, file)}
	}
	return out
}

func loadJig(fsys fs.FS, file, stem, source string) (Jig, error) {
	raw, err := fs.ReadFile(fsys, file)
	if err != nil {
		return Jig{}, err
	}
	var j Jig
	if err := decodeStrict(raw, &j); err != nil {
		return Jig{}, err
	}
	if j.Name != stem {
		return Jig{}, fmt.Errorf("name %q must match the file name %q", j.Name, stem)
	}
	j.Source = source
	return j, nil
}

var paramRef = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// validate checks the Jig on its own and against the loaded Guild catalogue.
func (j Jig) validate(guilds map[string]Guild) error {
	if !nameRE.MatchString(j.Name) {
		return errors.New("name must be lower-case letters, digits and hyphens")
	}
	if strings.TrimSpace(j.Description) == "" {
		return errors.New("a description is required, for the Archwright to choose the Jig by")
	}
	if len(j.Steps) == 0 {
		return errors.New("a Jig needs at least one step")
	}
	params := map[string]bool{}
	for _, p := range j.Parameters {
		if p.Name == "" || params[p.Name] {
			return fmt.Errorf("parameter %q is unnamed or duplicated", p.Name)
		}
		params[p.Name] = true
	}
	for _, s := range j.Steps {
		if s.Task == "" {
			return fmt.Errorf("step %q has no task", s.ID)
		}
		if _, ok := guilds[s.Guild]; !ok {
			return fmt.Errorf("step %q names unknown Guild %q", s.ID, s.Guild)
		}
		for _, m := range paramRef.FindAllStringSubmatch(s.Task, -1) {
			if !params[m[1]] {
				return fmt.Errorf("step %q task uses undeclared parameter %q", s.ID, m[1])
			}
		}
	}
	return store.ValidateSteps(j.plan(nil))
}

func (j Jig) plan(vals map[string]string) []store.Step {
	steps := make([]store.Step, 0, len(j.Steps))
	for _, s := range j.Steps {
		task := paramRef.ReplaceAllStringFunc(s.Task, func(m string) string {
			return vals[paramRef.FindStringSubmatch(m)[1]]
		})
		brief := task
		if len(s.Inputs) > 0 {
			brief += "\n\nInputs: " + strings.Join(s.Inputs, ", ")
		}
		if len(s.Outputs) > 0 {
			brief += "\n\nOutputs: " + strings.Join(s.Outputs, ", ")
		}
		title, _, _ := strings.Cut(strings.TrimSpace(task), "\n")
		if r := []rune(title); len(r) > 80 {
			title = string(r[:80])
		}
		if title == "" {
			title = s.ID
		}
		steps = append(steps, store.Step{ID: s.ID, Guild: s.Guild, Title: title, BlockedBy: s.BlockedBy, Checkpoint: s.Checkpoint, Brief: brief})
	}
	return steps
}

// Instantiate turns the Jig into Plan steps, filling its parameters.
func (j Jig) Instantiate(params map[string]string) ([]store.Step, error) {
	vals := map[string]string{}
	for _, p := range j.Parameters {
		vals[p.Name] = p.Default
	}
	declared := map[string]Param{}
	for _, p := range j.Parameters {
		declared[p.Name] = p
	}
	var unknown []string
	for k, v := range params {
		if _, ok := declared[k]; !ok {
			unknown = append(unknown, k)
			continue
		}
		vals[k] = v
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("jig %q has no parameter %s", j.Name, strings.Join(unknown, ", "))
	}
	for _, p := range j.Parameters {
		if p.Required && strings.TrimSpace(vals[p.Name]) == "" {
			return nil, fmt.Errorf("jig %q needs parameter %q", j.Name, p.Name)
		}
	}
	return j.plan(vals), nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
