// Package sites registers git repositories as Sites by remote URL, keeps the
// Works' own clone of each, and manages the worktrees Wrights work in, all
// created from that clone and never from a developer's checkout (ADR 0008,
// 0010, 0019, 0030).
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

	"github.com/phallatlassian/archwright/internal/remote"
	"github.com/phallatlassian/archwright/internal/store"
)

// ErrUnregistered means a directory's origin is not a registered Site.
var ErrUnregistered = errors.New("not a registered Site (a Site is matched by its git origin URL); register it by URL with `archwright site register --url`")

// Manager registers Sites and manages their clones and worktrees. Git
// operations on clones are serialised: they share each clone's metadata.
type Manager struct {
	st     *store.Store
	root   string // where worktrees live: outside every Site
	clones string // where the Works' clones live: <clones>/<site>/main (ADR 0030)
	mu     sync.Mutex

	// lookPath finds an executable on PATH; injectable so detection does not
	// depend on the machine's installed tools.
	lookPath func(string) (string, error)
}

// New returns a Manager keeping worktrees under root and clones under clones.
func New(st *store.Store, root, clones string) *Manager {
	return &Manager{st: st, root: root, clones: clones, lookPath: exec.LookPath}
}

// Proposal is what Detect infers for a remote; the developer confirms it.
type Proposal struct {
	Name           string      `json:"name"`
	URL            string      `json:"url"`
	BaseBranch     string      `json:"base_branch"`
	ConcurrencyCap int         `json:"concurrency_cap"`
	Gates          store.Gates `json:"gates"`
}

// RegisterInput is a confirmed registration. Gates must be present (even if
// empty commands) so defaults are never stored unconfirmed.
type RegisterInput struct {
	Name           string       `json:"name"`
	URL            string       `json:"url"`
	BaseBranch     string       `json:"base_branch"`
	ConcurrencyCap int          `json:"concurrency_cap"`
	Gates          *store.Gates `json:"gates"`
}

// remoteDefaultBranch asks the remote which branch its HEAD names. It also
// proves the remote is reachable with the developer's existing credentials.
func (m *Manager) remoteDefaultBranch(rawURL string) (string, error) {
	if err := os.MkdirAll(m.clones, 0o700); err != nil {
		return "", err
	}
	out, err := git(m.clones, "ls-remote", "--symref", "--", rawURL, "HEAD")
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", rawURL, err)
	}
	for _, line := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			if name, _, ok := strings.Cut(ref, "\t"); ok {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("%s has no default branch; give the base branch explicitly", rawURL)
}

// mainDir is where a Site's main checkout lives, under the Site's own folder
// in the clones directory (ADR 0030).
func mainDir(clones, site string) string { return filepath.Join(clones, site, "main") }

// legacyClone is where a Site's bare clone lived before ADR 0030.
func legacyClone(clones, site string) string { return filepath.Join(clones, site+".git") }

// cloneMain makes a normal clone of rawURL at dir (<clones>/<site>/main), the
// Site's main checkout: every branch is fetched into refs/remotes/origin/*,
// HEAD is detached at the base branch and no local branch is left, so nothing
// is ever checked out there that a later fetch would have to move. It does not
// check that the base branch exists.
func cloneMain(dir, rawURL, base string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if _, err := git(filepath.Dir(dir), "clone", "--quiet", "--no-checkout", "--no-tags", "--origin", "origin", "--", rawURL, filepath.Base(dir)); err != nil {
		return fmt.Errorf("cannot clone %s: %w", rawURL, err)
	}
	ref := "refs/remotes/origin/" + base
	if !gitOK(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}") {
		return fmt.Errorf("base branch %q does not exist on %s", base, rawURL)
	}
	if _, err := git(dir, "checkout", "--quiet", "--detach", ref); err != nil {
		return err
	}
	out, err := git(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return err
	}
	for _, b := range strings.Fields(out) { // the branch the clone began on
		if _, err := git(dir, "branch", "-D", "--", b); err != nil {
			return err
		}
	}
	return nil
}

// syncMain moves the main checkout to the freshly fetched base branch. It is
// never edited, so forcing loses nothing. A failure leaves it where it was:
// worktrees are made from a commit, not from the main checkout's HEAD.
func syncMain(site store.Site) {
	_, _ = git(site.ClonePath, "checkout", "--quiet", "--detach", "--force", "refs/remotes/origin/"+site.BaseBranch)
}

// fetchBare makes dir a bare repository whose origin is rawURL and fetches
// into it with the given extra options (default: every branch into
// refs/remotes/origin/*, which `remote add` configures).
func fetchBare(dir, rawURL string, opts []string, refspecs ...string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := git(dir, "init", "--bare", "--quiet"); err != nil {
		return err
	}
	if _, err := git(dir, "remote", "add", "origin", rawURL); err != nil {
		return err
	}
	if _, err := git(dir, append(append(append([]string{"fetch", "--quiet", "--no-tags"}, opts...), "origin"), refspecs...)...); err != nil {
		return fmt.Errorf("cannot fetch %s: %w", rawURL, err)
	}
	return nil
}

// Detect proposes registration values for the remote at rawURL. It clones
// into a temporary directory to read the base branch's tree, and deletes the
// clone before returning (ADR 0019).
func (m *Manager) Detect(rawURL string) (Proposal, error) {
	return m.detect(rawURL, "")
}

func (m *Manager) detect(rawURL, base string) (Proposal, error) {
	if _, err := remote.Normalize(rawURL); err != nil {
		return Proposal{}, err
	}
	if base == "" {
		var err error
		if base, err = m.remoteDefaultBranch(rawURL); err != nil {
			return Proposal{}, err
		}
	}
	p := Proposal{Name: SanitizeName(remote.Name(rawURL)), URL: rawURL, BaseBranch: base, ConcurrencyCap: store.DefaultConcurrencyCap}
	if err := os.MkdirAll(m.clones, 0o700); err != nil {
		return Proposal{}, err
	}
	tmp, err := os.MkdirTemp(m.clones, ".detect-")
	if err != nil {
		return Proposal{}, err
	}
	defer os.RemoveAll(tmp)
	if err := fetchBare(tmp, rawURL, []string{"--depth=1"}, "+refs/heads/"+base+":refs/remotes/origin/"+base); err != nil {
		return Proposal{}, err
	}
	tree := "refs/remotes/origin/" + base
	if !gitOK(tmp, "rev-parse", "--verify", "--quiet", tree+"^{commit}") {
		return Proposal{}, fmt.Errorf("base branch %q does not exist on %s", base, rawURL)
	}
	p.Gates = m.detectGates(func(name string) (string, bool) {
		out, err := git(tmp, "show", tree+":"+name)
		return out, err == nil
	})
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
// project's own Makefile targets for any gate the markers leave empty. read
// returns a file's content from the base branch's tree.
func (m *Manager) detectGates(read func(name string) (string, bool)) store.Gates {
	exists := func(name string) bool { _, ok := read(name); return ok }
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
	mk, _ := read("Makefile")
	targets := makeTargets(mk)
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
func makeTargets(makefile string) map[string]bool {
	targets := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(makefile))
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

// Register clones the repository at in.URL into the Works' own directory and
// records it as a Site. Doing so is the trust act for the repository's hooks
// and MCP config (ADR 0010). Nothing is left behind if it fails.
func (m *Manager) Register(in RegisterInput) (store.Site, error) {
	if _, err := remote.Normalize(in.URL); err != nil {
		return store.Site{}, err
	}
	// Fill anything left blank from the remote; explicit values always win.
	// Detected gate commands are never accepted silently (ADR 0010).
	if in.Name == "" {
		in.Name = SanitizeName(remote.Name(in.URL))
	}
	if !store.ValidSiteName(in.Name) {
		return store.Site{}, fmt.Errorf("site name %q must be lowercase letters, digits, '.', '_' or '-'", in.Name)
	}
	if in.BaseBranch == "" {
		var err error
		if in.BaseBranch, err = m.remoteDefaultBranch(in.URL); err != nil {
			return store.Site{}, err
		}
	}
	if err := validBranch(in.BaseBranch); err != nil {
		return store.Site{}, err
	}
	if in.Gates == nil {
		prop, err := m.detect(in.URL, in.BaseBranch)
		if err != nil {
			return store.Site{}, err
		}
		g, _ := json.Marshal(prop.Gates)
		return store.Site{}, fmt.Errorf("gates must be confirmed: detected %s; resubmit them explicitly (empty strings for none)", g)
	}
	if in.ConcurrencyCap == 0 {
		in.ConcurrencyCap = store.DefaultConcurrencyCap
	}
	if err := m.refuseDuplicate(in.Name, in.URL); err != nil { // early, before any network work
		return store.Site{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.refuseDuplicate(in.Name, in.URL); err != nil { // again under the lock: a concurrent Register may have won
		return store.Site{}, err
	}
	if err := os.MkdirAll(m.clones, 0o700); err != nil {
		return store.Site{}, err
	}
	clone := mainDir(m.clones, in.Name)
	for _, debris := range []string{filepath.Dir(clone), legacyClone(m.clones, in.Name)} { // of a crashed registration, or a removed Site: ours, and no Site claims it
		if err := os.RemoveAll(debris); err != nil {
			return store.Site{}, err
		}
	}
	site, err := m.cloneAndRecord(in, clone)
	if err != nil {
		os.RemoveAll(filepath.Dir(clone))
		return store.Site{}, err
	}
	return site, nil
}

func (m *Manager) cloneAndRecord(in RegisterInput, clone string) (store.Site, error) {
	if err := cloneMain(clone, in.URL, in.BaseBranch); err != nil {
		return store.Site{}, err
	}
	return m.st.AddSite(store.Site{
		Name: in.Name, URL: in.URL, ClonePath: clone, BaseBranch: in.BaseBranch,
		ConcurrencyCap: in.ConcurrencyCap, Gates: *in.Gates,
	})
}

// refuseDuplicate fails when the name or the
// normalised URL is taken.
func (m *Manager) refuseDuplicate(name, rawURL string) error {
	if s, err := m.st.SiteForURL(rawURL); err == nil {
		return fmt.Errorf("site %q (%s) is already registered for %s: %w", s.Name, s.URL, rawURL, store.ErrExists)
	}
	if s, err := m.st.GetSite(name); err == nil {
		return fmt.Errorf("site %q (%s): %w", s.Name, s.URL, store.ErrExists)
	}
	return nil
}

// validBranch refuses base branch names git would treat as options.
func validBranch(b string) error {
	if b == "" || strings.HasPrefix(b, "-") || strings.ContainsAny(b, " \t\n:^~?*[\\") || strings.Contains(b, "..") {
		return fmt.Errorf("invalid base branch %q", b)
	}
	return nil
}

// Remove unregisters a Site and deletes the Works' clone of it (its whole
// folder in the clones directory). The Site's
// Jobs and their history are kept (ADR 0017). It is refused while a Job still
// has worktrees from the clone.
func (m *Manager) Remove(name string) (store.Site, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, err := m.st.RemoveSite(name)
	if err != nil {
		return store.Site{}, err
	}
	if !inside(site.ClonePath, m.clones) || site.ClonePath == m.clones {
		return site, fmt.Errorf("site %q unregistered, but its clone %s is outside %s and was left alone", name, site.ClonePath, m.clones)
	}
	folder := cloneFolder(site)
	if !inside(folder, m.clones) || folder == m.clones {
		return site, fmt.Errorf("site %q unregistered, but its clone folder %s is outside %s and was left alone", name, folder, m.clones)
	}
	if err := os.RemoveAll(folder); err != nil {
		return site, fmt.Errorf("site %q unregistered, but deleting its clone failed: %w", name, err)
	}
	return site, nil
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

// Resolve returns the Site a directory belongs to: the one whose remote is
// the directory's own `origin`, however either is spelled. A directory with no
// origin, or whose origin is not registered, is refused (ADR 0019). This reads
// the developer's git config; it never writes to it.
func (m *Manager) Resolve(path string) (store.Site, error) {
	if !filepath.IsAbs(path) {
		return store.Site{}, fmt.Errorf("path %q must be absolute", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return store.Site{}, fmt.Errorf("%s: %w", path, ErrUnregistered)
	}
	origin, err := git(real, "config", "--local", "--get", "remote.origin.url")
	if err != nil || origin == "" {
		return store.Site{}, fmt.Errorf("%s has no git origin: %w", real, ErrUnregistered)
	}
	site, err := m.st.SiteForURL(origin)
	if errors.Is(err, store.ErrNotFound) {
		return store.Site{}, fmt.Errorf("%s (origin %s): %w", real, origin, ErrUnregistered)
	}
	return site, err
}

// cloneFolder is what Remove deletes: the Site's folder holding its main
// checkout, or the bare clone itself for a Site not yet migrated.
func cloneFolder(site store.Site) string {
	if filepath.Base(site.ClonePath) == "main" {
		return filepath.Dir(site.ClonePath)
	}
	return site.ClonePath
}
