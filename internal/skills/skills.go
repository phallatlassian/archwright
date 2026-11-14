// Package skills delivers the factory's skills to runs (ADR 0005, 0018). The
// mattpocock skills are vendored here as a snapshot pinned to an upstream
// commit and are never fetched at runtime; the Warden-shaped ones are forks
// owned by the factory. Before a run the Works copies the Guild's skills into
// the run's worktree and hides them from git (deliver.go).
package skills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed all:vendor
var vendorFS embed.FS

// Provenance records where the vendored snapshot came from (vendor/PROVENANCE.json).
type Provenance struct {
	Source     string            `json:"source"`
	Commit     string            `json:"commit"` // the full upstream commit SHA
	CommitDate string            `json:"commit_date"`
	VendoredOn string            `json:"vendored_on"`
	License    string            `json:"license"`
	Files      map[string]string `json:"files"` // sha256 of every vendored upstream file, by path under upstream/
	Forks      []Fork            `json:"forks"`
}

// Fork is a skill the factory owns, edited from an upstream skill.
type Fork struct {
	Name          string            `json:"name"`
	Upstream      string            `json:"upstream"`
	Edited        string            `json:"edited"`
	UpstreamFiles map[string]string `json:"upstream_files"` // the upstream files it was forked from
}

// Skill is one skill's files, rooted at the skill directory.
type Skill struct {
	Name string
	FS   fs.FS
}

// Catalogue is the vendored and forked skills available to Guilds.
type Catalogue struct {
	fsys fs.FS // PROVENANCE.json, upstream/<skill>/... and forks/<skill>/...
}

// Vendored is the catalogue embedded in the binary.
func Vendored() *Catalogue {
	sub, err := fs.Sub(vendorFS, "vendor")
	if err != nil {
		panic(err)
	}
	return &Catalogue{fsys: sub}
}

// Provenance reads the recorded provenance of the snapshot.
func (c *Catalogue) Provenance() (Provenance, error) {
	raw, err := fs.ReadFile(c.fsys, "PROVENANCE.json")
	if err != nil {
		return Provenance{}, err
	}
	var p Provenance
	if err := json.Unmarshal(raw, &p); err != nil {
		return Provenance{}, fmt.Errorf("PROVENANCE.json: %w", err)
	}
	return p, nil
}

// Skill returns one skill: the factory's fork if there is one, else the
// vendored upstream skill.
func (c *Catalogue) Skill(name string) (Skill, error) {
	if !validName(name) {
		return Skill{}, fmt.Errorf("invalid skill name %q", name)
	}
	for _, dir := range []string{"forks", "upstream"} {
		sub, err := fs.Sub(c.fsys, path.Join(dir, name))
		if err != nil {
			return Skill{}, err
		}
		if _, err := fs.Stat(sub, "SKILL.md"); err == nil {
			return Skill{Name: name, FS: sub}, nil
		}
	}
	return Skill{}, fmt.Errorf("no skill %q in the vendored catalogue", name)
}

// Select returns the named skills, failing on the first unknown one.
func (c *Catalogue) Select(names []string) ([]Skill, error) {
	out := make([]Skill, 0, len(names))
	for _, n := range names {
		s, err := c.Skill(n)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Verify checks the embedded upstream snapshot against its recorded hashes:
// no vendored file may be edited, added or missing without re-vendoring.
func (c *Catalogue) Verify() error {
	p, err := c.Provenance()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var bad []string
	err = fs.WalkDir(c.fsys, "upstream", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(name, "upstream/")
		seen[rel] = true
		b, err := fs.ReadFile(c.fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		switch want, ok := p.Files[rel]; {
		case !ok:
			bad = append(bad, "unrecorded file "+rel)
		case want != hex.EncodeToString(sum[:]):
			bad = append(bad, "modified file "+rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for rel := range p.Files {
		if !seen[rel] {
			bad = append(bad, "missing file "+rel)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("vendored skills differ from their provenance (commit %s): %s", p.Commit, strings.Join(bad, "; "))
	}
	return nil
}
