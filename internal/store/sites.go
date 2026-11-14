package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/phallatlassian/archwright/internal/remote"
)

// ErrExists means a Site with that name or URL is already registered.
var ErrExists = errors.New("already registered")

// DefaultConcurrencyCap is the per-Site run cap when none is given (ADR 0008).
const DefaultConcurrencyCap = 3

var siteName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidSiteName reports whether name can be a Site name (it also names the
// Site's clone directory, so it must not be able to escape it).
func ValidSiteName(name string) bool { return siteName.MatchString(name) }

// Gates are a Site's check commands. The Works re-runs them on handback; they
// live in the registry so a Wright's branch cannot weaken them (ADR 0010).
type Gates struct {
	Typecheck string `json:"typecheck"`
	Lint      string `json:"lint"`
	Test      string `json:"test"`
}

// Site is a git repository registered with the Works by remote URL; the
// Works works only in its own clone of it (ADR 0019).
type Site struct {
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	ClonePath      string    `json:"clone_path"`
	BaseBranch     string    `json:"base_branch"`
	ConcurrencyCap int       `json:"concurrency_cap"`
	Gates          Gates     `json:"gates"`
	CreatedAt      time.Time `json:"created_at"`
}

// AddSite records a Site. The caller has already cloned the repository. A Site
// is unique by normalised URL; a name left behind by a removed or path-era
// Site is reused, so its Jobs' history stays attached to it.
func (s *Store) AddSite(site Site) (Site, error) {
	if !siteName.MatchString(site.Name) {
		return Site{}, fmt.Errorf("site name %q must be lowercase letters, digits, '.', '_' or '-'", site.Name)
	}
	if site.URL == "" || site.ClonePath == "" || site.BaseBranch == "" {
		return Site{}, errors.New("site url, clone path and base branch are required")
	}
	key, err := remote.Normalize(site.URL)
	if err != nil {
		return Site{}, err
	}
	if site.ConcurrencyCap < 1 {
		return Site{}, errors.New("concurrency cap must be at least 1")
	}
	raw, err := json.Marshal(site.Gates)
	if err != nil {
		return Site{}, err
	}
	now := s.now().UTC()
	site.CreatedAt = now
	at := now.Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return Site{}, err
	}
	defer tx.Rollback()
	var clash string
	err = tx.QueryRow("SELECT name FROM sites WHERE unregistered_at IS NULL AND (name = ? OR url_key = ?)", site.Name, key).Scan(&clash)
	if err == nil {
		return Site{}, fmt.Errorf("site %q (%s): %w", clash, site.URL, ErrExists)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Site{}, err
	}
	var old string
	err = tx.QueryRow("SELECT name FROM sites WHERE name = ?", site.Name).Scan(&old)
	switch {
	case err == nil: // an unregistered row: register it again under the same name
		_, err = tx.Exec(`UPDATE sites SET url = ?, url_key = ?, clone_path = ?, base_branch = ?, concurrency_cap = ?, gates = ?,
			created_at = ?, unregistered_at = NULL WHERE name = ?`,
			site.URL, key, site.ClonePath, site.BaseBranch, site.ConcurrencyCap, string(raw), at, site.Name)
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.Exec(`INSERT INTO sites (name, url, url_key, clone_path, base_branch, concurrency_cap, gates, created_at)
			VALUES (?,?,?,?,?,?,?,?)`, site.Name, site.URL, key, site.ClonePath, site.BaseBranch, site.ConcurrencyCap, string(raw), at)
	}
	if err != nil {
		return Site{}, err
	}
	if err := appendEvent(tx, at, "", "site.registered", map[string]string{"name": site.Name, "url": site.URL}); err != nil {
		return Site{}, err
	}
	return site, tx.Commit()
}

// RemoveSite unregisters a Site and returns what it was. Its Jobs, worktree
// records and events stay (ADR 0017): the row is kept, marked unregistered,
// and no longer resolves. The caller deletes the clone. A Site whose Jobs
// still have worktrees is refused.
func (s *Store) RemoveSite(name string) (Site, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Site{}, err
	}
	defer tx.Rollback()
	site, err := scanSite(tx.QueryRow("SELECT "+siteCols+" FROM sites WHERE name = ? AND unregistered_at IS NULL", name))
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, fmt.Errorf("site %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Site{}, err
	}
	var live int
	if err := tx.QueryRow("SELECT COUNT(*) FROM worktrees WHERE site = ? AND removed_at IS NULL", name).Scan(&live); err != nil {
		return Site{}, err
	}
	if live > 0 {
		return Site{}, fmt.Errorf("site %q still has %d worktrees in use; close its Jobs first", name, live)
	}
	at := s.stamp()
	if _, err := tx.Exec("UPDATE sites SET url_key = NULL, clone_path = ?, unregistered_at = ? WHERE name = ?", "unregistered:"+name, at, name); err != nil {
		return Site{}, err
	}
	if err := appendEvent(tx, at, "", "site.removed", map[string]string{"name": name, "url": site.URL}); err != nil {
		return Site{}, err
	}
	return site, tx.Commit()
}

const siteCols = "name, url, clone_path, base_branch, concurrency_cap, gates, created_at"

type scanner interface{ Scan(...any) error }

func scanSite(r scanner) (Site, error) {
	var st Site
	var gates, at string
	if err := r.Scan(&st.Name, &st.URL, &st.ClonePath, &st.BaseBranch, &st.ConcurrencyCap, &gates, &at); err != nil {
		return Site{}, err
	}
	if err := json.Unmarshal([]byte(gates), &st.Gates); err != nil {
		return Site{}, err
	}
	st.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return st, nil
}

// GetSite reads one registered Site by name. A Site that was removed, or was
// registered by path before ADR 0019, is refused as not registered.
func (s *Store) GetSite(name string) (Site, error) {
	st, err := scanSite(s.db.QueryRow("SELECT "+siteCols+" FROM sites WHERE name = ? AND unregistered_at IS NULL", name))
	if errors.Is(err, sql.ErrNoRows) {
		var n int
		if s.db.QueryRow("SELECT COUNT(*) FROM sites WHERE name = ?", name).Scan(&n) == nil && n > 0 {
			return Site{}, fmt.Errorf("site %q is no longer registered (removed, or registered by path before Sites were registered by URL); re-register it with `archwright site register --url`: %w", name, ErrNotFound)
		}
		return Site{}, fmt.Errorf("site %q: %w", name, ErrNotFound)
	}
	return st, err
}

// ListSites returns all registered Sites by name.
func (s *Store) ListSites() ([]Site, error) {
	rows, err := s.db.Query("SELECT " + siteCols + " FROM sites WHERE unregistered_at IS NULL ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sites := []Site{}
	for rows.Next() {
		st, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		sites = append(sites, st)
	}
	return sites, rows.Err()
}

// SiteForURL returns the registered Site whose remote is the same repository
// as rawURL, however it is spelled.
func (s *Store) SiteForURL(rawURL string) (Site, error) {
	key, err := remote.Normalize(rawURL)
	if err != nil {
		return Site{}, fmt.Errorf("%s: %w", rawURL, ErrNotFound)
	}
	st, err := scanSite(s.db.QueryRow("SELECT "+siteCols+" FROM sites WHERE url_key = ?", key))
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, fmt.Errorf("no Site is registered for %s: %w", rawURL, ErrNotFound)
	}
	return st, err
}

// Worktree is a Works-managed git worktree belonging to a Job.
type Worktree struct {
	ID        string     `json:"id"`
	JobID     string     `json:"job_id"`
	Site      string     `json:"site"`
	Kind      string     `json:"kind"` // integration, wright (writing) or readonly (detached)
	Path      string     `json:"path"`
	Branch    string     `json:"branch,omitempty"`
	BaseRef   string     `json:"base_ref"`
	BaseSHA   string     `json:"base_sha"` // commit the worktree started from
	CreatedAt time.Time  `json:"created_at"`
	RemovedAt *time.Time `json:"removed_at,omitempty"`
}

// Worktree kinds.
const (
	KindIntegration = "integration"
	KindWright      = "wright"
	KindReadOnly    = "readonly"
)

// NewWorktreeID returns a fresh worktree id.
func NewWorktreeID() string { return newID("wt-") }

// AddWorktree records a created worktree and a worktree.created event.
func (s *Store) AddWorktree(w Worktree) (Worktree, error) {
	now := s.now().UTC()
	w.CreatedAt = now
	at := now.Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return Worktree{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO worktrees (id, job_id, site, kind, path, branch, base_ref, base_sha, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, w.ID, w.JobID, w.Site, w.Kind, w.Path, w.Branch, w.BaseRef, w.BaseSHA, at); err != nil {
		return Worktree{}, err
	}
	if err := appendEvent(tx, at, w.JobID, "worktree.created", map[string]string{"id": w.ID, "kind": w.Kind, "branch": w.Branch}); err != nil {
		return Worktree{}, err
	}
	return w, tx.Commit()
}

// MarkWorktreeRemoved stamps a worktree as removed and records why.
func (s *Store) MarkWorktreeRemoved(w Worktree, reason string) error {
	at := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE worktrees SET removed_at = ? WHERE id = ?", at, w.ID); err != nil {
		return err
	}
	if err := appendEvent(tx, at, w.JobID, "worktree.removed", map[string]string{"id": w.ID, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

// Worktrees lists a Job's worktrees, including removed ones, oldest first.
func (s *Store) Worktrees(jobID string) ([]Worktree, error) {
	rows, err := s.db.Query(`SELECT id, job_id, site, kind, path, branch, base_ref, base_sha, created_at, removed_at
		FROM worktrees WHERE job_id = ? ORDER BY created_at, id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Worktree{}
	for rows.Next() {
		var w Worktree
		var at string
		var removed sql.NullString
		if err := rows.Scan(&w.ID, &w.JobID, &w.Site, &w.Kind, &w.Path, &w.Branch, &w.BaseRef, &w.BaseSHA, &at, &removed); err != nil {
			return nil, err
		}
		w.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		if removed.Valid {
			t, _ := time.Parse(time.RFC3339Nano, removed.String)
			w.RemovedAt = &t
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CreateJobOn is CreateJob for a Job that belongs to a registered Site.
func (s *Store) CreateJobOn(site, title, description string) (Job, error) {
	if _, err := s.GetSite(site); err != nil {
		return Job{}, err
	}
	return s.createJob(site, title, description)
}

// CloseJob records that the developer accepted or discarded a Job.
func (s *Store) CloseJob(jobID, outcome string) error {
	if outcome != "accepted" && outcome != "discarded" {
		return fmt.Errorf("outcome must be accepted or discarded, not %q", outcome)
	}
	at := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE jobs SET outcome = ?, ended_at = ? WHERE id = ?", outcome, at, jobID); err != nil {
		return err
	}
	if err := appendEvent(tx, at, jobID, "job."+outcome, map[string]string{}); err != nil {
		return err
	}
	return tx.Commit()
}
