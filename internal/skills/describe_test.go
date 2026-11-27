package skills

import (
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/phallatlassian/archwright/internal/defs"
)

func mapSkill(name, skillMD, openaiYAML string) Skill {
	m := fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}}
	if openaiYAML != "" {
		m["agents/openai.yaml"] = &fstest.MapFile{Data: []byte(openaiYAML)}
	}
	return Skill{Name: name, FS: m}
}

func TestDescribeReadsTheOneLineDescription(t *testing.T) {
	folded := mapSkill("a", "---\nname: a\ndescription: >-\n  First line\n  second line.\n---\nbody\n", "")
	quoted := mapSkill("b", "---\nname: b\ndescription: \"Quoted: yes.\"\n---\nbody\n", "")
	got, err := Describe([]Skill{folded, quoted}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	want := []Info{{Name: "a", Description: "First line second line."}, {Name: "b", Description: "Quoted: yes."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, err := Describe([]Skill{mapSkill("c", "no frontmatter", "")}, "claude"); err == nil {
		t.Error("a SKILL.md without frontmatter was accepted")
	}
	if _, err := Describe([]Skill{mapSkill("d", "---\nname: d\n---\n", "")}, "claude"); err == nil {
		t.Error("a SKILL.md without a description was accepted")
	}
}

func TestDescribeLeavesOutSkillsTheRunnersAgentCannotInvoke(t *testing.T) {
	open := mapSkill("open", "---\nname: open\ndescription: Open.\n---\n", "policy:\n  allow_implicit_invocation: true\n")
	noModel := mapSkill("no-model", "---\nname: no-model\ndescription: Hidden from the model.\ndisable-model-invocation: true\n---\n", "")
	noImplicit := mapSkill("no-implicit", "---\nname: no-implicit\ndescription: Hidden from codex.\n---\n", "policy:\n  allow_implicit_invocation: false\n")

	names := func(runner string) []string {
		infos, err := Describe([]Skill{open, noModel, noImplicit}, runner)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, i := range infos {
			out = append(out, i.Name)
		}
		return out
	}
	if got, want := names("claude"), []string{"open", "no-implicit"}; !reflect.DeepEqual(got, want) {
		t.Errorf("claude: %v, want %v", got, want)
	}
	if got, want := names("codex"), []string{"open", "no-model"}; !reflect.DeepEqual(got, want) {
		t.Errorf("codex: %v, want %v", got, want)
	}
}

func TestDisablingFlagsNamesEachWayASkillIsHiddenFromTheAgent(t *testing.T) {
	both := mapSkill("both", "---\nname: both\ndescription: x\ndisable-model-invocation: true\n---\n", "policy:\n  allow_implicit_invocation: false\n")
	flags, err := DisablingFlags(both)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 2 {
		t.Errorf("flags = %v", flags)
	}
	clean := mapSkill("clean", "---\nname: clean\ndescription: x\ndisable-model-invocation: false\n---\n", "policy:\n  allow_implicit_invocation: true\n")
	if flags, _ := DisablingFlags(clean); len(flags) != 0 {
		t.Errorf("flags = %v", flags)
	}
}

// The Guilds whose Wardens and Wrights are meant to invoke their skills (#160,
// #163) ship none that is flagged against the agent.
var skillUsingGuilds = []string{"masonry", "survey", "hallmark", "chancery"}

func shippedGuildSkills(t *testing.T, guild string) []Skill {
	t.Helper()
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g, ok := reg.Guild(guild)
	if !ok {
		t.Fatalf("no shipped guild %s", guild)
	}
	set, err := Vendored().Select(g.Skills)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestSkillsOfTheSkillUsingGuildsAreInvocableByTheAgent(t *testing.T) {
	for _, guild := range skillUsingGuilds {
		set := shippedGuildSkills(t, guild)
		if len(set) == 0 {
			t.Errorf("%s has no skills", guild)
		}
		for _, s := range set {
			if flags, err := DisablingFlags(s); err != nil || len(flags) > 0 {
				t.Errorf("%s: skill %s is flagged against the agent: %v %v", guild, s.Name, flags, err)
			}
		}
		for _, runner := range []string{"claude", "codex"} {
			if got, err := Describe(set, runner); err != nil || len(got) != len(set) {
				t.Errorf("%s on %s: %d of %d skills described: %v", guild, runner, len(got), len(set), err)
			}
		}
	}
}

func TestChancerysSkillsAreTheOnesItCanRunWithoutATracker(t *testing.T) {
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g, _ := reg.Guild("chancery")
	if want := []string{"to-spec", "to-tickets", "wayfinder", "grill-with-docs", "grilling", "domain-modeling"}; !reflect.DeepEqual(g.Skills, want) {
		t.Errorf("chancery skills = %v, want %v", g.Skills, want)
	}
	if len(g.UserOnly) != 0 {
		t.Errorf("chancery marks skills user-only: %v", g.UserOnly)
	}
}

func TestMasonrysSkillsAreTheOnesTheFactoryCanRun(t *testing.T) {
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g, _ := reg.Guild("masonry")
	if want := []string{"implement", "implement-spec", "tdd", "codebase-design", "pr", "diagnosing-bugs"}; !reflect.DeepEqual(g.Skills, want) {
		t.Errorf("masonry skills = %v, want %v", g.Skills, want)
	}
}

// referencedSkills names the other skills a skill's text tells the agent to
// use: a Skill tool call, a "the X skill" mention or a /X command.
func referencedSkills(text string, known []string) []string {
	var out []string
	for _, n := range known {
		q := regexp.QuoteMeta(n)
		re := regexp.MustCompile("(?i)(" +
			`skill tool (with|for|on) [` + "`" + `"']?` + q + `\b|` +
			"`" + q + "`" + ` skill|` +
			`\bthe ` + q + ` skill|` +
			`(^|[\s(` + "`" + `])/` + q + `\b)`)
		if re.MatchString(text) {
			out = append(out, n)
		}
	}
	return out
}

func TestReferencedSkillsFindsEachWayASkillCallsAnother(t *testing.T) {
	known := []string{"tdd", "code-review", "pr"}
	for text, want := range map[string][]string{
		`call the Skill tool with "tdd" where possible`:            {"tdd"},
		"see the `code-review` skill":                              {"code-review"},
		"then run /pr to open it":                                  {"pr"},
		"the `pr` fork says not to use gh":                         nil,
		"under src/pr/ and a tdd-style test":                       nil,
		"dispatch a review piece for the integration branch":       nil,
		"Use the code-review skill, then the tdd skill":            {"tdd", "code-review"},
		"the Skill tool with `tdd`; and the Skill tool for \"pr\"": {"tdd", "pr"},
	} {
		got := referencedSkills(text, known)
		if !sameSet(got, want) {
			t.Errorf("%q: got %v, want %v", text, got, want)
		}
	}
}

func sameSet(a, b []string) bool {
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestNoSkillOfAGuildCallsASkillOutsideItsSet(t *testing.T) {
	c := Vendored()
	prov, err := c.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	known := make([]string, 0, len(prov.Files))
	seen := map[string]bool{}
	for p := range prov.Files {
		if n, _, ok := strings.Cut(p, "/"); ok && !seen[n] {
			seen[n] = true
			known = append(known, n)
		}
	}
	for _, f := range prov.Forks {
		if !seen[f.Name] {
			seen[f.Name] = true
			known = append(known, f.Name)
		}
	}
	for _, guild := range skillUsingGuilds {
		set := shippedGuildSkills(t, guild)
		in := map[string]bool{}
		for _, s := range set {
			in[s.Name] = true
		}
		for _, s := range set {
			for _, text := range markdownOf(t, s) {
				for _, ref := range referencedSkills(text, known) {
					if ref != s.Name && !in[ref] {
						t.Errorf("%s: skill %s calls %s, which is not in the Guild's set", guild, s.Name, ref)
					}
				}
			}
		}
	}
}

// markdownOf is the text of every Markdown file in the skill.
func markdownOf(t *testing.T, s Skill) []string {
	t.Helper()
	var out []string
	err := walkFiles(s, func(name string, b []byte) {
		if strings.HasSuffix(name, ".md") {
			out = append(out, string(b))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func walkFiles(s Skill, visit func(name string, b []byte)) error {
	return fs.WalkDir(s.FS, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(s.FS, name)
		if err != nil {
			return err
		}
		visit(name, b)
		return nil
	})
}
