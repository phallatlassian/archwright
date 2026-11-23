package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/phallatlassian/archwright/internal/defs"
)

func TestProvenanceIsRecorded(t *testing.T) {
	p, err := Vendored().Provenance()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.Commit) {
		t.Errorf("commit %q is not a full SHA", p.Commit)
	}
	if !strings.HasPrefix(p.Source, "https://github.com/mattpocock/skills") {
		t.Errorf("source %q", p.Source)
	}
	if p.CommitDate == "" || p.VendoredOn == "" || len(p.Files) == 0 {
		t.Errorf("incomplete provenance: %+v", p)
	}
}

func TestVendoredSnapshotMatchesItsProvenance(t *testing.T) {
	if err := Vendored().Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyCatchesTampering(t *testing.T) {
	good := "good\n"
	sum := sha256.Sum256([]byte(good))
	prov := `{"commit":"` + strings.Repeat("a", 40) + `","files":{"tdd/SKILL.md":"` + hex.EncodeToString(sum[:]) + `"}}`
	fresh := func() fstest.MapFS {
		return fstest.MapFS{
			"PROVENANCE.json":       {Data: []byte(prov)},
			"upstream/tdd/SKILL.md": {Data: []byte(good)},
		}
	}
	if err := (&Catalogue{fsys: fresh()}).Verify(); err != nil {
		t.Fatalf("control: %v", err)
	}
	for name, mutate := range map[string]func(fstest.MapFS){
		"edited":  func(m fstest.MapFS) { m["upstream/tdd/SKILL.md"] = &fstest.MapFile{Data: []byte("evil")} },
		"added":   func(m fstest.MapFS) { m["upstream/tdd/extra.md"] = &fstest.MapFile{Data: []byte("x")} },
		"removed": func(m fstest.MapFS) { delete(m, "upstream/tdd/SKILL.md") },
	} {
		t.Run(name, func(t *testing.T) {
			m := fresh()
			mutate(m)
			if err := (&Catalogue{fsys: m}).Verify(); err == nil {
				t.Error("tampering went unnoticed")
			}
		})
	}
}

func TestForksOverrideUpstreamAndRecordTheirBase(t *testing.T) {
	c := Vendored()
	prov, _ := c.Provenance()
	forked := map[string]Fork{}
	for _, f := range prov.Forks {
		forked[f.Name] = f
		if len(f.UpstreamFiles) == 0 {
			t.Errorf("fork %s records no upstream hashes", f.Name)
		}
		if len(strings.Fields(f.Edited)) < 5 {
			t.Errorf("fork %s has no edit note: %q", f.Name, f.Edited)
		}
		// the base hashes are the upstream files the fork was made from, all of them
		for rel, sum := range f.UpstreamFiles {
			if prov.Files[rel] != sum {
				t.Errorf("fork %s records %s as %s, but the vendored upstream is %s", f.Name, rel, sum, prov.Files[rel])
			}
		}
		for rel := range prov.Files {
			if strings.HasPrefix(rel, f.Name+"/") && f.UpstreamFiles[rel] == "" {
				t.Errorf("fork %s does not record its upstream %s", f.Name, rel)
			}
		}
	}
	// Forks the Works dispatches through, and forks a Wright follows.
	for _, name := range []string{"implement-spec", "code-review", "triage", "pr", "implement", "tdd", "codebase-design", "research"} {
		if _, ok := forked[name]; !ok {
			t.Errorf("%s is not recorded as a fork", name)
		}
		fork, err := c.Skill(name)
		if err != nil {
			t.Fatal(err)
		}
		b, err := readFile(fork, "SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		up, _ := os.ReadFile(filepath.Join("vendor", "upstream", name, "SKILL.md"))
		if string(b) == string(up) || !strings.Contains(string(b), "Factory fork of upstream `"+name+"`") {
			t.Errorf("%s is not the factory's fork", name)
		}
	}
	for _, name := range []string{"implement-spec", "code-review", "triage", "pr", "implement"} {
		fork, _ := c.Skill(name)
		b, _ := readFile(fork, "SKILL.md")
		if !strings.Contains(string(b), "archwright tool") {
			t.Errorf("%s does not use the Works' tools", name)
		}
	}
	// diagnosing-bugs and prototype wait for #164 and #165: still verbatim upstream.
	for _, name := range []string{"diagnosing-bugs", "prototype"} {
		skill, err := c.Skill(name)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := readFile(skill, "SKILL.md")
		up, _ := os.ReadFile(filepath.Join("vendor", "upstream", name, "SKILL.md"))
		if string(b) != string(up) {
			t.Errorf("%s is forked, but it is vendored verbatim until its grilling ticket is decided", name)
		}
		if _, ok := forked[name]; ok {
			t.Errorf("%s is recorded as a fork", name)
		}
	}
	if _, err := c.Skill("no-such-skill"); err == nil {
		t.Error("unknown skill")
	}
}

// Upstream implement tells the agent to do five things a Wright must not
// (#160); the fork must not carry them.
func TestImplementForkLacksTheInstructionsAWrightCannotFollow(t *testing.T) {
	fork, err := Vendored().Skill("implement")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := readFile(fork, "SKILL.md")
	text := string(b)
	for _, banned := range []string{
		"fetch it from the issue tracker",
		"Run typechecking regularly, single test files regularly",
		`call the Skill tool with "code-review"`,
		"Commit your work to the current branch",
		"If the reference is ambiguous, ask.",
	} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(banned)) {
			t.Errorf("implement still says %q", banned)
		}
	}
	for _, want := range []string{"read_issue", "run_gate", "archwright tool ask", "Never run `git add` or `git commit`"} {
		if !strings.Contains(text, want) {
			t.Errorf("implement does not say %q", want)
		}
	}
	if strings.Contains(text, "code-review") {
		t.Error("implement names code-review, which is not in Masonry's set")
	}
}

func TestResearchForkDoesTheResearchItself(t *testing.T) {
	fork, err := Vendored().Skill("research")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := readFile(fork, "SKILL.md")
	if strings.Contains(strings.ToLower(string(b)), "spin up") {
		t.Error("research still spins up an agent")
	}
	if !strings.Contains(string(b), "no background agent") {
		t.Error("research does not say the run does the research itself")
	}
}

func TestEveryShippedGuildSkillResolves(t *testing.T) {
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	c := Vendored()
	for _, g := range reg.Guilds() {
		if _, err := c.Select(g.Skills); err != nil {
			t.Errorf("guild %s: %v", g.Name, err)
		}
	}
}

func readFile(s Skill, name string) ([]byte, error) { return fs.ReadFile(s.FS, name) }
