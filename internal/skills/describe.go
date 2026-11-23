package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"
)

// Info is what an agent is told about a skill it can invoke: its name and the
// one-line description from its SKILL.md.
type Info struct {
	Name        string
	Description string
}

// Describe returns the skills the runner's agent can invoke, in order, each
// with its description. A skill hidden from the agent is left out: the agent
// could not call it, and listing it would send the agent to a blocked call.
// Which flag hides a skill depends on the runner (see invocable).
func Describe(set []Skill, runner string) ([]Info, error) {
	var out []Info
	for _, s := range set {
		meta, err := readMeta(s)
		if err != nil {
			return nil, err
		}
		if !meta.invocableBy(runner) {
			continue
		}
		out = append(out, Info{Name: s.Name, Description: meta.description})
	}
	return out, nil
}

// DisablingFlags names every flag that hides the skill from the agent, for
// either runner: "disable-model-invocation" in SKILL.md (Claude Code) and
// "allow_implicit_invocation: false" in agents/openai.yaml (Codex).
func DisablingFlags(s Skill) ([]string, error) {
	meta, err := readMeta(s)
	if err != nil {
		return nil, err
	}
	var flags []string
	if meta.noModelInvocation {
		flags = append(flags, "disable-model-invocation")
	}
	if meta.noImplicitInvocation {
		flags = append(flags, "allow_implicit_invocation: false")
	}
	return flags, nil
}

type meta struct {
	description          string
	noModelInvocation    bool // disable-model-invocation: true in SKILL.md
	noImplicitInvocation bool // policy.allow_implicit_invocation: false in agents/openai.yaml
}

// invocableBy reports whether the runner's agent can call the skill itself:
// Claude Code reads SKILL.md's disable-model-invocation, Codex reads
// agents/openai.yaml's allow_implicit_invocation.
func (m meta) invocableBy(runner string) bool {
	if runner == "codex" {
		return !m.noImplicitInvocation
	}
	return !m.noModelInvocation
}

func readMeta(s Skill) (meta, error) {
	raw, err := fs.ReadFile(s.FS, "SKILL.md")
	if err != nil {
		return meta{}, fmt.Errorf("skill %s: %w", s.Name, err)
	}
	front, ok := frontmatter(raw)
	if !ok {
		return meta{}, fmt.Errorf("skill %s: SKILL.md must start with a --- frontmatter block", s.Name)
	}
	var fm struct {
		Description       string `yaml:"description"`
		NoModelInvocation bool   `yaml:"disable-model-invocation"`
	}
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return meta{}, fmt.Errorf("skill %s: SKILL.md frontmatter: %w", s.Name, err)
	}
	desc := strings.Join(strings.Fields(fm.Description), " ")
	if desc == "" {
		return meta{}, fmt.Errorf("skill %s: SKILL.md has no description", s.Name)
	}
	m := meta{description: desc, noModelInvocation: fm.NoModelInvocation}

	yml, err := fs.ReadFile(s.FS, "agents/openai.yaml")
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return meta{}, fmt.Errorf("skill %s: %w", s.Name, err)
	default:
		var oa struct {
			Policy struct {
				AllowImplicitInvocation *bool `yaml:"allow_implicit_invocation"`
			} `yaml:"policy"`
		}
		if err := yaml.Unmarshal(yml, &oa); err != nil {
			return meta{}, fmt.Errorf("skill %s: agents/openai.yaml: %w", s.Name, err)
		}
		m.noImplicitInvocation = oa.Policy.AllowImplicitInvocation != nil && !*oa.Policy.AllowImplicitInvocation
	}
	return m, nil
}

// frontmatter returns the YAML between the leading --- lines of a SKILL.md.
func frontmatter(b []byte) ([]byte, bool) {
	rest, ok := bytes.CutPrefix(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("---\n"))
	if !ok {
		return nil, false
	}
	front, _, ok := bytes.Cut(rest, []byte("\n---"))
	return front, ok
}
