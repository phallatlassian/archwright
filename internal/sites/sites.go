// Package sites registers git repositories as Sites and manages the
// worktrees Wrights work in (ADR 0008, 0010).
package sites

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/phallatlassian/archwright/internal/store"
)

// ErrUnregistered means a path is not inside any registered Site.
var ErrUnregistered = errors.New("not a registered Site; register it with `archwright site register`")

// Manager registers Sites and manages their worktrees. Git operations on
// worktrees are serialised: they share each repository's metadata.
type Manager struct {
	st   *store.Store
	root string // where worktrees live: outside every Site
	mu   sync.Mutex

	// lookPath finds an executable on PATH; injectable so detection does not
	// depend on the machine's installed tools.
	lookPath func(string) (string, error)
}

// New returns a Manager keeping worktrees under root.
func New(st *store.Store, root string) *Manager {
	return &Manager{st: st, root: root, lookPath: exec.LookPath}
}

// Proposal is what Detect infers for a repository; the developer confirms it.
type Proposal struct {
	Name           string      `json:"name"`
	Path           string      `json:"path"`
	BaseBranch     string      `json:"base_branch"`
	Remote         string      `json:"remote"`
	ConcurrencyCap int         `json:"concurrency_cap"`
	Gates          store.Gates `json:"gates"`
}

// RegisterInput is a confirmed registration. Gates must be present (even if
// empty commands) so defaults are never stored unconfirmed.
type RegisterInput struct {
	Name           string       `json:"name"`
	Path           string       `json:"path"`
	BaseBranch     string       `json:"base_branch"`
	Remote         string       `json:"remote"`
	ConcurrencyCap int          `json:"concurrency_cap"`
	Gates          *store.Gates `json:"gates"`
}

// repoRoot resolves path to the canonical top level of the git repository it is.
func repoRoot(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("site path %q must be absolute", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	top, err := git(real, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not a git repository", real)
	}
	if top, err = filepath.EvalSymlinks(top); err != nil {
		return "", err
	}
	if top != real {
		return "", fmt.Errorf("%s is inside a repository; register its root %s", real, top)
	}
	return real, nil
}

// Detect proposes registration values for the repository at path.
func (m *Manager) Detect(path string) (Proposal, error) {
	root, err := repoRoot(path)
	if err != nil {
		return Proposal{}, err
	}
	p := Proposal{Name: SanitizeName(filepath.Base(root)), Path: root, ConcurrencyCap: store.DefaultConcurrencyCap}
	if gitOK(root, "remote", "get-url", "origin") {
		p.Remote = "origin"
		if ref, err := git(root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
			p.BaseBranch = strings.TrimPrefix(ref, "origin/")
		}
	}
	if p.BaseBranch == "" {
		p.BaseBranch, _ = git(root, "symbolic-ref", "--short", "HEAD")
	}
	p.Gates = m.detectGates(root)
	return p, nil
}

// SanitizeName turns a directory name into a valid Site name.
func SanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.TrimLeft(b.String(), ".-_")
}

// detectGates proposes gate commands: a marker file's defaults first, then the
// project's own Makefile targets for any gate the markers leave empty.
func (m *Manager) detectGates(root string) store.Gates {
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(root, name)); return err == nil }
	var g store.Gates
	switch {
	case exists("go.mod"):
		g = store.Gates{Typecheck: "go vet ./...", Test: "go test ./..."}
		if _, err := m.lookPath("golangci-lint"); err == nil {
			g.Lint = "golangci-lint run"
		}
	case exists("package.json"):
		g = store.Gates{Typecheck: "npm run typecheck", Lint: "npm run lint", Test: "npm test"}
	case exists("Cargo.toml"):
		g = store.Gates{Typecheck: "cargo check", Lint: "cargo clippy", Test: "cargo test"}
	}
	targets := makeTargets(filepath.Join(root, "Makefile"))
	fill := func(dst *string, names ...string) {
		if *dst != "" {
			return
		}
		for _, n := range names {
			if targets[n] {
				*dst = "make " + n
				return
			}
		}
	}
	fill(&g.Typecheck, "typecheck", "vet")
	fill(&g.Lint, "lint")
	fill(&g.Test, "test")
	return g
}

// makeTargets conservatively lists the targets a Makefile defines: lines
// starting with a plain name and a colon. Make is never executed.
func makeTargets(path string) map[string]bool {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	targets := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || name == "" || strings.HasPrefix(rest, "=") { // `X := y` is a variable
			continue
		}
		valid := true
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				valid = false
				break
			}
		}
		if valid {
			targets[name] = true
		}
	}
	return targets
}

// Register records the repository at in.Path as a Site. Doing so is the
// trust act for the repository's hooks and MCP config (ADR 0010).
func (m *Manager) Register(in RegisterInput) (store.Site, error) {
	root, err := repoRoot(in.Path)
	if err != nil {
		return store.Site{}, err
	}
	// Fill anything left blank from detection; explicit values always win.
	// Detected gate commands are never accepted silently (ADR 0010).
	prop, err := m.Detect(root)
	if err != nil {
		return store.Site{}, err
	}
	if in.Name == "" {
		in.Name = prop.Name
	}
	if in.BaseBranch == "" {
		in.BaseBranch = prop.BaseBranch
	}
	if in.Remote == "" {
		in.Remote = prop.Remote
	}
	if in.Gates == nil {
		g, _ := json.Marshal(prop.Gates)
		return store.Site{}, fmt.Errorf("gates must be confirmed: detected %s; resubmit them explicitly (empty strings for none)", g)
	}
	wtRoot, err := m.realRoot()
	if err != nil {
		return store.Site{}, err
	}
	if inside(root, wtRoot) || inside(wtRoot, root) {
		return store.Site{}, fmt.Errorf("%s overlaps the Works' worktree directory %s", root, wtRoot)
	}
	if in.BaseBranch == "" {
		return store.Site{}, errors.New("base branch is required")
	}
	if !gitOK(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+in.BaseBranch) {
		return store.Site{}, fmt.Errorf("base branch %q does not exist in %s", in.BaseBranch, root)
	}
	if in.Remote != "" && !gitOK(root, "remote", "get-url", in.Remote) {
		return store.Site{}, fmt.Errorf("remote %q does not exist in %s", in.Remote, root)
	}
	if in.ConcurrencyCap == 0 {
		in.ConcurrencyCap = store.DefaultConcurrencyCap
	}
	return m.st.AddSite(store.Site{
		Name: in.Name, Path: root, BaseBranch: in.BaseBranch, Remote: in.Remote,
		ConcurrencyCap: in.ConcurrencyCap, Gates: *in.Gates,
	})
}

// realRoot is the worktree directory with symlinks resolved, created if needed,
// so containment checks compare real paths.
func (m *Manager) realRoot() (string, error) {
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(m.root)
}

func inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

// Resolve returns the Site that path belongs to, refusing unregistered paths.
func (m *Manager) Resolve(path string) (store.Site, error) {
	if !filepath.IsAbs(path) {
		return store.Site{}, fmt.Errorf("path %q must be absolute", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return store.Site{}, fmt.Errorf("%s: %w", path, ErrUnregistered)
	}
	site, err := m.st.SiteForPath(real)
	if errors.Is(err, store.ErrNotFound) {
		return store.Site{}, fmt.Errorf("%s: %w", real, ErrUnregistered)
	}
	return site, err
}
