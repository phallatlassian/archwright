package store

import (
	"encoding/json"
	"time"
)

// Needs-you item kinds.
const NeedsScopeOverlap = "scope-overlap"

// NeedsYouItem is one entry in the Needs-you inbox.
type NeedsYouItem struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	JobID     string          `json:"job_id"`
	Site      string          `json:"site,omitempty"`
	Summary   string          `json:"summary"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt time.Time       `json:"created_at"`
}

// NewNeedsYou is an item as its raiser supplies it. DedupeKey makes raising
// the same condition twice a no-op.
type NewNeedsYou struct {
	Kind, JobID, Site, Summary, DedupeKey string
	Detail                                any
}

// AddNeedsYou files an item in the inbox and reports whether it was new.
func (s *Store) AddNeedsYou(n NewNeedsYou) (bool, error) {
	detail, err := json.Marshal(n.Detail)
	if err != nil {
		return false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	at := s.stamp()
	res, err := tx.Exec("INSERT OR IGNORE INTO needs_you (kind, job_id, site, summary, detail, dedupe_key, created_at) VALUES (?,?,?,?,?,?,?)",
		n.Kind, n.JobID, n.Site, n.Summary, string(detail), n.DedupeKey, at)
	if err != nil {
		return false, err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return false, nil
	}
	if err := appendEvent(tx, at, n.JobID, "needs_you."+n.Kind, map[string]string{"summary": n.Summary}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// NeedsYou lists the inbox, oldest first.
func (s *Store) NeedsYou() ([]NeedsYouItem, error) {
	rows, err := s.db.Query("SELECT id, kind, job_id, site, summary, detail, created_at FROM needs_you ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []NeedsYouItem{}
	for rows.Next() {
		var it NeedsYouItem
		var detail, at string
		if err := rows.Scan(&it.ID, &it.Kind, &it.JobID, &it.Site, &it.Summary, &detail, &at); err != nil {
			return nil, err
		}
		it.Detail = json.RawMessage(detail)
		it.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		items = append(items, it)
	}
	return items, rows.Err()
}

// ScopedBrief is the brief of another open Job's latest dispatch of a live piece.
type ScopedBrief struct {
	JobID, Piece string
	Brief        json.RawMessage
}

// SiteBriefs returns the briefs of the latest dispatch of every live piece
// of the other open Jobs on site, for comparing declared scopes.
func (s *Store) SiteBriefs(site, exceptJob string) ([]ScopedBrief, error) {
	rows, err := s.db.Query(`SELECT d.job_id, d.piece, d.brief FROM dispatches d
		JOIN jobs j ON j.id = d.job_id
		JOIN pieces p ON p.job_id = d.job_id AND p.piece = d.piece
		WHERE j.site = ? AND j.id <> ? AND COALESCE(j.outcome,'') = '' AND p.status <> ?
		AND d.round = (SELECT MAX(round) FROM dispatches WHERE job_id = d.job_id AND piece = d.piece)
		ORDER BY d.rowid`, site, exceptJob, PieceFailed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopedBrief
	for rows.Next() {
		var b ScopedBrief
		var raw string
		if err := rows.Scan(&b.JobID, &b.Piece, &raw); err != nil {
			return nil, err
		}
		b.Brief = json.RawMessage(raw)
		out = append(out, b)
	}
	return out, rows.Err()
}
