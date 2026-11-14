package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RunRecord is what the dashboard knows of one run: whose it was, how it
// ended and what it consumed.
type RunRecord struct {
	RunID        string     `json:"run_id"`
	JobID        string     `json:"job_id"`
	DispatchID   string     `json:"dispatch_id,omitempty"`
	Guild        string     `json:"guild,omitempty"`
	Runner       string     `json:"runner,omitempty"`
	InputTokens  int        `json:"input_tokens"`
	OutputTokens int        `json:"output_tokens"`
	CostUSD      float64    `json:"cost_usd"`
	Status       string     `json:"status"` // running until it ends, then done, failed or cancelled
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
}

// RunEvent is one entry of a run's timeline. Raw is served apart, on demand.
type RunEvent struct {
	RunID   string    `json:"run_id"`
	Seq     int       `json:"seq"`
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
	HasRaw  bool      `json:"has_raw"` // false once the raw content is pruned
	At      time.Time `json:"at"`
}

// UsageRow is usage and cost totalled for one Job, Guild and runner.
type UsageRow struct {
	JobID        string  `json:"job_id"`
	Guild        string  `json:"guild"`
	Runner       string  `json:"runner"`
	Runs         int     `json:"runs"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// StartRunRecord notes that a run began.
func (s *Store) StartRunRecord(runID, jobID, dispatchID, guild, runner string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := s.stamp()
	if _, err := tx.Exec("INSERT INTO run_records (run_id, job_id, dispatch_id, guild, runner, started_at) VALUES (?,?,?,?,?,?)",
		runID, jobID, dispatchID, guild, runner, at); err != nil {
		return err
	}
	if err := appendEvent(tx, at, jobID, "run.started", map[string]string{"run": runID, "guild": guild}); err != nil {
		return err
	}
	return tx.Commit()
}

// SetRunUsage records what a run consumed and which runner ran it.
func (s *Store) SetRunUsage(runID, runner string, in, out int, costUSD float64) error {
	res, err := s.db.Exec("UPDATE run_records SET runner = CASE WHEN ? <> '' THEN ? ELSE runner END, input_tokens = ?, output_tokens = ?, cost_usd = ? WHERE run_id = ?",
		runner, runner, in, out, costUSD, runID)
	return mustChange(res, err, runID)
}

// EndRunRecord notes how a run ended.
func (s *Store) EndRunRecord(runID, status string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := s.stamp()
	res, err := tx.Exec("UPDATE run_records SET status = ?, ended_at = ? WHERE run_id = ?", status, at, runID)
	if err := mustChange(res, err, runID); err != nil {
		return err
	}
	var jobID string
	if err := tx.QueryRow("SELECT job_id FROM run_records WHERE run_id = ?", runID).Scan(&jobID); err != nil {
		return err
	}
	if err := appendEvent(tx, at, jobID, "run.ended", map[string]string{"run": runID, "status": status}); err != nil {
		return err
	}
	return tx.Commit()
}

// LastEventID is the id of the newest event, 0 when there are none: where a
// reader that wants only what happens from now on starts.
func (s *Store) LastEventID() (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT COALESCE(MAX(id), 0) FROM events").Scan(&id)
	return id, err
}

func mustChange(res sql.Result, err error, runID string) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("run %q: %w", runID, ErrNotFound)
	}
	return nil
}

// AddRunEvent appends one entry to a run's timeline and returns its seq.
func (s *Store) AddRunEvent(runID, kind, summary string, raw json.RawMessage) (int, error) {
	var seq int
	err := s.db.QueryRow(`INSERT INTO run_events (run_id, seq, kind, summary, raw, at)
		VALUES (?, (SELECT COALESCE(MAX(seq)+1, 1) FROM run_events WHERE run_id = ?), ?, ?, ?, ?) RETURNING seq`,
		runID, runID, kind, summary, string(raw), s.stamp()).Scan(&seq)
	return seq, err
}

const runCols = "run_id, job_id, dispatch_id, guild, runner, input_tokens, output_tokens, cost_usd, status, started_at, COALESCE(ended_at,'')"

func scanRun(r scanner) (RunRecord, error) {
	var rec RunRecord
	var start, end string
	if err := r.Scan(&rec.RunID, &rec.JobID, &rec.DispatchID, &rec.Guild, &rec.Runner, &rec.InputTokens, &rec.OutputTokens, &rec.CostUSD, &rec.Status, &start, &end); err != nil {
		return RunRecord{}, err
	}
	rec.StartedAt, _ = time.Parse(time.RFC3339Nano, start)
	if end != "" {
		t, _ := time.Parse(time.RFC3339Nano, end)
		rec.EndedAt = &t
	}
	return rec, nil
}

// RunRecords lists a Job's runs, oldest first.
func (s *Store) RunRecords(jobID string) ([]RunRecord, error) {
	rows, err := s.db.Query("SELECT "+runCols+" FROM run_records WHERE job_id = ? ORDER BY started_at, rowid", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunRecord{}
	for rows.Next() {
		rec, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetRunRecord reads one run.
func (s *Store) GetRunRecord(runID string) (RunRecord, error) {
	rec, err := scanRun(s.db.QueryRow("SELECT "+runCols+" FROM run_records WHERE run_id = ?", runID))
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, fmt.Errorf("run %q: %w", runID, ErrNotFound)
	}
	return rec, err
}

// RunEvents is a run's timeline, without raw content.
func (s *Store) RunEvents(runID string) ([]RunEvent, error) {
	if _, err := s.GetRunRecord(runID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query("SELECT seq, kind, summary, raw <> '', at FROM run_events WHERE run_id = ? ORDER BY seq", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunEvent{}
	for rows.Next() {
		e := RunEvent{RunID: runID}
		var at string
		if err := rows.Scan(&e.Seq, &e.Kind, &e.Summary, &e.HasRaw, &at); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// RunEventRaw returns the vendor event behind one timeline entry. Once the
// raw content is pruned (ADR 0017) it is gone and this says so.
func (s *Store) RunEventRaw(runID string, seq int) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRow("SELECT raw FROM run_events WHERE run_id = ? AND seq = ?", runID, seq).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("run %q event %d: %w", runID, seq, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, fmt.Errorf("the raw content of run %q event %d was pruned: %w", runID, seq, ErrNotFound)
	}
	return json.RawMessage(raw), nil
}

// Usage totals usage and cost per Job, Guild and runner.
func (s *Store) Usage() ([]UsageRow, error) {
	rows, err := s.db.Query(`SELECT job_id, guild, runner, COUNT(*), SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
		FROM run_records GROUP BY job_id, guild, runner ORDER BY job_id, guild, runner`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.JobID, &u.Guild, &u.Runner, &u.Runs, &u.InputTokens, &u.OutputTokens, &u.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
