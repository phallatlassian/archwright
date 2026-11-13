package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Gate names, in the order they run.
const (
	GateTypecheck = "typecheck"
	GateLint      = "lint"
	GateTest      = "test"
)

// Gate result statuses.
const (
	GatePassed  = "passed"
	GateFailed  = "failed"
	GateSkipped = "skipped" // no command is configured for the gate
)

// Gate run statuses: running until the Works finishes, then passed or failed,
// or not_run when there was nothing to run the gates on or no gates to run.
const (
	GateRunRunning = "running"
	GateRunPassed  = "passed"
	GateRunFailed  = "failed"
	GateRunNotRun  = "not_run"
)

// GateResult is the outcome of one gate command.
type GateResult struct {
	Gate       string `json:"gate"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output,omitempty"` // the tail of combined stdout and stderr
	DurationMS int64  `json:"duration_ms"`
}

// GateRun is the Works' own record of re-running a Site's gates on a handback
// branch. The Archwright trusts it over any Warden claim (ADR 0006, 0010).
type GateRun struct {
	HandbackID int64        `json:"handback_id"`
	JobID      string       `json:"job_id"`
	Status     string       `json:"status"`
	Branch     string       `json:"branch,omitempty"`
	SHA        string       `json:"sha,omitempty"` // the commit the gates ran on
	Reason     string       `json:"reason,omitempty"`
	Results    []GateResult `json:"results"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
}

// FinishGateRun records the outcome of a handback's gate run, once.
func (s *Store) FinishGateRun(run GateRun) error {
	if run.Status == GateRunRunning {
		return errors.New("a finished gate run cannot be running")
	}
	if run.Results == nil {
		run.Results = []GateResult{}
	}
	raw, err := json.Marshal(run.Results)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := s.stamp()
	res, err := tx.Exec(`UPDATE gate_runs SET status = ?, branch = ?, sha = ?, reason = ?, results = ?, finished_at = ?
		WHERE handback_id = ? AND status = ?`, run.Status, run.Branch, run.SHA, run.Reason, string(raw), at, run.HandbackID, GateRunRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("gate run for handback %d is not running: %w", run.HandbackID, ErrConflict)
	}
	if err := appendEvent(tx, at, run.JobID, "gates."+run.Status, map[string]any{"handback_id": run.HandbackID, "sha": run.SHA}); err != nil {
		return err
	}
	return tx.Commit()
}

// InterruptGateRuns finishes runs a stopped Works left running, as not_run,
// so nothing reads as passing that never finished.
func (s *Store) InterruptGateRuns() error {
	_, err := s.db.Exec("UPDATE gate_runs SET status = ?, reason = ?, finished_at = ? WHERE status = ?",
		GateRunNotRun, "the Works stopped before the gates finished", s.stamp(), GateRunRunning)
	return err
}

// GateRunFor reads the gate run recorded for a handback.
func (s *Store) GateRunFor(handbackID int64) (GateRun, error) {
	var g GateRun
	var results, started string
	var finished sql.NullString
	err := s.db.QueryRow(`SELECT handback_id, job_id, status, branch, sha, reason, results, started_at, finished_at
		FROM gate_runs WHERE handback_id = ?`, handbackID).Scan(&g.HandbackID, &g.JobID, &g.Status, &g.Branch, &g.SHA, &g.Reason, &results, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return GateRun{}, fmt.Errorf("gate run for handback %d: %w", handbackID, ErrNotFound)
	}
	if err != nil {
		return GateRun{}, err
	}
	if err := json.Unmarshal([]byte(results), &g.Results); err != nil {
		return GateRun{}, err
	}
	g.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if finished.Valid {
		t, _ := time.Parse(time.RFC3339Nano, finished.String)
		g.FinishedAt = &t
	}
	return g, nil
}

// Gate change statuses.
const (
	ChangePending  = "pending"
	ChangeApproved = "approved"
	ChangeRejected = "rejected"
)

// GateChange is a proposed replacement of a Site's gate commands. It takes
// effect only when the developer approves it (ADR 0010).
type GateChange struct {
	ID        string     `json:"id"`
	Site      string     `json:"site"`
	Gates     Gates      `json:"gates"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

// ProposeGates records a pending change to a Site's gate commands. The
// registry is untouched until ApproveGateChange.
func (s *Store) ProposeGates(site string, g Gates) (GateChange, error) {
	cur, err := s.GetSite(site)
	if err != nil {
		return GateChange{}, err
	}
	if cur.Gates == g {
		return GateChange{}, fmt.Errorf("site %q already has these gates: %w", site, ErrConflict)
	}
	raw, _ := json.Marshal(g)
	now := s.now().UTC()
	c := GateChange{ID: newID("gc-"), Site: site, Gates: g, Status: ChangePending, CreatedAt: now}
	tx, err := s.db.Begin()
	if err != nil {
		return GateChange{}, err
	}
	defer tx.Rollback()
	at := now.Format(time.RFC3339Nano)
	if _, err := tx.Exec("INSERT INTO gate_changes (id, site, gates, status, created_at) VALUES (?,?,?,?,?)", c.ID, site, string(raw), c.Status, at); err != nil {
		return GateChange{}, err
	}
	if err := appendEvent(tx, at, "", "gates.change_proposed", map[string]string{"id": c.ID, "site": site}); err != nil {
		return GateChange{}, err
	}
	return c, tx.Commit()
}

const changeCols = "id, site, gates, status, created_at, decided_at"

func scanChange(r scanner) (GateChange, error) {
	var c GateChange
	var gates, created string
	var decided sql.NullString
	if err := r.Scan(&c.ID, &c.Site, &gates, &c.Status, &created, &decided); err != nil {
		return GateChange{}, err
	}
	if err := json.Unmarshal([]byte(gates), &c.Gates); err != nil {
		return GateChange{}, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if decided.Valid {
		t, _ := time.Parse(time.RFC3339Nano, decided.String)
		c.DecidedAt = &t
	}
	return c, nil
}

// GateChanges lists proposed changes, newest last; status "" lists all.
func (s *Store) GateChanges(status string) ([]GateChange, error) {
	rows, err := s.db.Query("SELECT "+changeCols+" FROM gate_changes WHERE ? = '' OR status = ? ORDER BY created_at, id", status, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GateChange{}
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ApproveGateChange applies a pending change to the Site's gates and rejects
// the Site's other pending changes, which were proposed against the old gates.
// It is the only way a Site's gates change after registration.
func (s *Store) ApproveGateChange(id string) (Site, error) {
	return s.decideGateChange(id, ChangeApproved)
}

// RejectGateChange discards a pending change.
func (s *Store) RejectGateChange(id string) error {
	_, err := s.decideGateChange(id, ChangeRejected)
	return err
}

func (s *Store) decideGateChange(id, outcome string) (Site, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Site{}, err
	}
	defer tx.Rollback()
	c, err := scanChange(tx.QueryRow("SELECT "+changeCols+" FROM gate_changes WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, fmt.Errorf("gate change %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Site{}, err
	}
	if c.Status != ChangePending {
		return Site{}, fmt.Errorf("gate change %q is already %s: %w", id, c.Status, ErrConflict)
	}
	at := s.stamp()
	if _, err := tx.Exec("UPDATE gate_changes SET status = ?, decided_at = ? WHERE id = ?", outcome, at, id); err != nil {
		return Site{}, err
	}
	if outcome == ChangeApproved {
		raw, _ := json.Marshal(c.Gates)
		if _, err := tx.Exec("UPDATE sites SET gates = ? WHERE name = ?", string(raw), c.Site); err != nil {
			return Site{}, err
		}
		if _, err := tx.Exec("UPDATE gate_changes SET status = ?, decided_at = ? WHERE site = ? AND status = ?", ChangeRejected, at, c.Site, ChangePending); err != nil {
			return Site{}, err
		}
	}
	if err := appendEvent(tx, at, "", "gates.change_"+outcome, map[string]string{"id": id, "site": c.Site}); err != nil {
		return Site{}, err
	}
	site, err := scanSite(tx.QueryRow("SELECT "+siteCols+" FROM sites WHERE name = ?", c.Site))
	if err != nil {
		return Site{}, err
	}
	return site, tx.Commit()
}
