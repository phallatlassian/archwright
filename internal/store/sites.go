package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrExists means a Site with that name or path is already registered.
var ErrExists = errors.New("already registered")

// DefaultConcurrencyCap is the per-Site run cap when none is given (ADR 0008).
const DefaultConcurrencyCap = 3

var siteName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Gates are a Site's check commands. The Works re-runs them on handback; they
// live in the registry so a Wright's branch cannot weaken them (ADR 0010).
type Gates struct {
	Typecheck string `json:"typecheck"`
	Lint      string `json:"lint"`
	Test      string `json:"test"`
}

// Site is a git repository registered with the Works.
type Site struct {
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	BaseBranch     string    `json:"base_branch"`
	Remote         string    `json:"remote"`
	ConcurrencyCap int       `json:"concurrency_cap"`
	Gates          Gates     `json:"gates"`
	CreatedAt      time.Time `json:"created_at"`
}

// AddSite records a Site. The caller has already checked the repository itself.
func (s *Store) AddSite(site Site) (Site, error) {
	if !siteName.MatchString(site.Name) {
		return Site{}, fmt.Errorf("site name %q must be lowercase letters, digits, '.', '_' or '-'", site.Name)
	}
	if site.Path == "" || site.BaseBranch == "" {
		return Site{}, errors.New("site path and base branch are required")
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
	err = tx.QueryRow("SELECT name FROM sites WHERE name = ? OR path = ?", site.Name, site.Path).Scan(&clash)
	if err == nil {
		return Site{}, fmt.Errorf("site %q (%s): %w", clash, site.Path, ErrExists)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Site{}, err
	}
	if _, err := tx.Exec(`INSERT INTO sites (name, path, base_branch, remote, concurrency_cap, gates, created_at)
		VALUES (?,?,?,?,?,?,?)`, site.Name, site.Path, site.BaseBranch, site.Remote, site.ConcurrencyCap, string(raw), at); err != nil {
		return Site{}, err
	}
	if err := appendEvent(tx, at, "", "site.registered", map[string]string{"name": site.Name, "path": site.Path}); err != nil {
		return Site{}, err
	}
	return site, tx.Commit()
}

const siteCols = "name, path, base_branch, remote, concurrency_cap, gates, created_at"

type scanner interface{ Scan(...any) error }

func scanSite(r scanner) (Site, error) {
	var st Site
	var gates, at string
	if err := r.Scan(&st.Name, &st.Path, &st.BaseBranch, &st.Remote, &st.ConcurrencyCap, &gates, &at); err != nil {
		return Site{}, err
	}
	if err := json.Unmarshal([]byte(gates), &st.Gates); err != nil {
		return Site{}, err
	}
	st.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return st, nil
}

// GetSite reads one Site by name.
func (s *Store) GetSite(name string) (Site, error) {
	st, err := scanSite(s.db.QueryRow("SELECT "+siteCols+" FROM sites WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, fmt.Errorf("site %q: %w", name, ErrNotFound)
	}
	return st, err
}

// ListSites returns all Sites by name.
func (s *Store) ListSites() ([]Site, error) {
	rows, err := s.db.Query("SELECT " + siteCols + " FROM sites ORDER BY name")
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

// SiteForPath returns the Site whose registered path is dir or contains it.
// dir must already be absolute with symlinks resolved.
func (s *Store) SiteForPath(dir string) (Site, error) {
	sites, err := s.ListSites()
	if err != nil {
		return Site{}, err
	}
	var best *Site // the longest match wins, so a Site nested in another resolves to itself
	for i, st := range sites {
		if (dir == st.Path || strings.HasPrefix(dir, strings.TrimSuffix(st.Path, "/")+"/")) && (best == nil || len(st.Path) > len(best.Path)) {
			best = &sites[i]
		}
	}
	if best != nil {
		return *best, nil
	}
	return Site{}, fmt.Errorf("%s is not inside a registered Site: %w", dir, ErrNotFound)
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
	if _, err := tx.Exec("UPDATE jobs SET outcome = ? WHERE id = ?", outcome, jobID); err != nil {
		return err
	}
	if err := appendEvent(tx, at, jobID, "job."+outcome, map[string]string{}); err != nil {
		return err
	}
	return tx.Commit()
}
