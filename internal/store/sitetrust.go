package store

import (
	"time"
)

// Trust probe outcomes, per Site and runner.
const (
	TrustCaptured    = "captured"    // a Session started in the Site's main checkout: folder trust (and Codex's hook trust) is given and the hooks reach the Works
	TrustPending     = "pending"     // the probe is waiting for the developer to answer the trust dialog
	TrustUnconfirmed = "unconfirmed" // the probe waited and no Session started: the dialog was not answered, or hooks are blocked
	TrustFailed      = "failed"      // the probe could not be run
	TrustUnavailable = "unavailable" // the runner is not installed
)

// NeedsTrustProbe is the kind of the Needs-you item that asks the developer to
// accept a runner's folder trust for a Site once. It belongs to the Site, not a Job.
const NeedsTrustProbe = "trust-probe"

// SiteTrust is what the latest trust probe of a Site found for one runner.
type SiteTrust struct {
	Site      string    `json:"site"`
	Runner    string    `json:"runner"`
	State     string    `json:"state"`
	Detail    string    `json:"detail,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// SetSiteTrust records the outcome of a probe, replacing the Site's earlier one for that runner.
func (s *Store) SetSiteTrust(site, runner, state, detail string) error {
	_, err := s.db.Exec(`INSERT INTO site_trust (site, runner, state, detail, checked_at) VALUES (?,?,?,?,?)
		ON CONFLICT(site, runner) DO UPDATE SET state = excluded.state, detail = excluded.detail, checked_at = excluded.checked_at`,
		site, runner, state, detail, s.stamp())
	return err
}

// SiteTrusts lists what the probes found, for one Site or (name "") every Site.
func (s *Store) SiteTrusts(site string) ([]SiteTrust, error) {
	rows, err := s.db.Query("SELECT site, runner, state, detail, checked_at FROM site_trust WHERE ? = '' OR site = ? ORDER BY site, runner", site, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SiteTrust
	for rows.Next() {
		var t SiteTrust
		var at string
		if err := rows.Scan(&t.Site, &t.Runner, &t.State, &t.Detail, &at); err != nil {
			return nil, err
		}
		t.CheckedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ResolveNeedsYou removes the item filed under dedupeKey, if any: its
// condition has cleared.
func (s *Store) ResolveNeedsYou(dedupeKey string) error {
	_, err := s.db.Exec("DELETE FROM needs_you WHERE dedupe_key = ?", dedupeKey)
	return err
}

// SiteTrusts lists what the probes found, for one Site or (name "") every Site.
func (s *Snapshot) SiteTrusts(site string) ([]SiteTrust, error) { return s.st.SiteTrusts(site) }
