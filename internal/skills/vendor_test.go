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
	forked := map[string]bool{}
	for _, f := range prov.Forks {
		forked[f.Name] = true
		if len(f.UpstreamFiles) == 0 {
			t.Errorf("fork %s records no upstream hashes", f.Name)
		}
	}
	for _, name := range []string{"implement-spec", "code-review", "triage"} {
		if !forked[name] {
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
		if string(b) == string(up) || !strings.Contains(string(b), "archwright tool") {
			t.Errorf("%s is not the Works-dispatching fork", name)
		}
	}
	tdd, _ := c.Skill("tdd")
	b, _ := readFile(tdd, "SKILL.md")
	up, _ := os.ReadFile(filepath.Join("vendor", "upstream", "tdd", "SKILL.md"))
	if string(b) != string(up) {
		t.Error("an unforked skill is vendored verbatim")
	}
	if _, err := c.Skill("no-such-skill"); err == nil {
		t.Error("unknown skill")
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
