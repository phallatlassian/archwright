package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Step run statuses. A Plan step with no StepRun is pending.
const (
	StepPending    = "pending"
	StepRunning    = "running"
	StepBlocked    = "blocked" // the Warden's blocking ask is open; the step waits for the answer (ADR 0015, 0029)
	StepHandedBack = "handed_back"
	StepFailed     = "failed"
)

// StepRun is a Plan step the Archwright started: the Warden's task brief and
// how the step ended (ADR 0020).
type StepRun struct {
	JobID      string     `json:"job_id"`
	Step       string     `json:"step"`
	Guild      string     `json:"guild"`
	Status     string     `json:"status"`
	Brief      string     `json:"brief"` // the task brief the Warden was summoned with
	HandbackID int64      `json:"handback_id,omitempty"`
	Reason     string     `json:"reason,omitempty"` // why a step failed
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

const stepCols = "job_id, step, guild, status, brief, COALESCE(handback_id,0), reason, started_at, finished_at"

type rowScanner interface{ Scan(dest ...any) error }

func scanStep(r rowScanner) (StepRun, error) {
	var s StepRun
	var started string
	var finished sql.NullString
	if err := r.Scan(&s.JobID, &s.Step, &s.Guild, &s.Status, &s.Brief, &s.HandbackID, &s.Reason, &started, &finished); err != nil {
		return StepRun{}, err
	}
	s.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if finished.Valid {
		t, _ := time.Parse(time.RFC3339Nano, finished.String)
		s.FinishedAt = &t
	}
	return s, nil
}

// StartStep records that a step's Warden is summoned with brief. A step that
// is running or handed back is refused; a failed one starts again. check, run
// inside the transaction after the Plan is read, may refuse the start (for an
// unfinished blocker), so the check and the start cannot be separated by
// another start.
func (s *Store) StartStep(jobID, stepID string, check func(plan Plan, runs []StepRun) (guild, brief string, err error)) (StepRun, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return StepRun{}, err
	}
	defer tx.Rollback()
	plan, err := getPlan(tx, jobID)
	if err != nil {
		return StepRun{}, err
	}
	runs, err := stepRuns(tx, jobID)
	if err != nil {
		return StepRun{}, err
	}
	for _, r := range runs {
		if r.Step == stepID && (r.Status == StepRunning || r.Status == StepBlocked || r.Status == StepHandedBack) {
			return StepRun{}, fmt.Errorf("step %q of job %q is %s: %w", stepID, jobID, r.Status, ErrConflict)
		}
	}
	guild, brief, err := check(plan, runs)
	if err != nil {
		return StepRun{}, err
	}
	at := s.stamp()
	if _, err := tx.Exec(`INSERT INTO steps (job_id, step, guild, status, brief, started_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT (job_id, step) DO UPDATE SET guild = excluded.guild, status = excluded.status, brief = excluded.brief,
		handback_id = NULL, reason = '', started_at = excluded.started_at, finished_at = NULL`,
		jobID, stepID, guild, StepRunning, brief, at); err != nil {
		return StepRun{}, err
	}
	if err := appendEvent(tx, at, jobID, "step.started", map[string]string{"step": stepID, "guild": guild}); err != nil {
		return StepRun{}, err
	}
	run, err := scanStep(tx.QueryRow("SELECT "+stepCols+" FROM steps WHERE job_id = ? AND step = ?", jobID, stepID))
	if err != nil {
		return StepRun{}, err
	}
	return run, tx.Commit()
}

func stepRuns(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, jobID string) ([]StepRun, error) {
	rows, err := q.Query("SELECT "+stepCols+" FROM steps WHERE job_id = ? ORDER BY started_at, step", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StepRun{}
	for rows.Next() {
		r, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StepRuns lists the steps of a Job that have been started.
func (s *Store) StepRuns(jobID string) ([]StepRun, error) { return stepRuns(s.db, jobID) }

// GetStepRun reads one started step.
func (s *Store) GetStepRun(jobID, stepID string) (StepRun, error) {
	r, err := scanStep(s.db.QueryRow("SELECT "+stepCols+" FROM steps WHERE job_id = ? AND step = ?", jobID, stepID))
	if errors.Is(err, sql.ErrNoRows) {
		return StepRun{}, fmt.Errorf("step %q of job %q has not been started: %w", stepID, jobID, ErrNotFound)
	}
	return r, err
}

// RunningSteps lists every started step still running, for the Works to
// re-summon their Wardens after a restart.
func (s *Store) RunningSteps() ([]StepRun, error) {
	rows, err := s.db.Query("SELECT " + stepCols + " FROM steps WHERE status = 'running' ORDER BY started_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StepRun{}
	for rows.Next() {
		r, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HandBackStep ends a running step with the Warden's handback and files the
// handback in the Archwright's inbox, in one transaction. A done or partial
// handback hands the step back; a failed one fails it.
func (s *Store) HandBackStep(jobID, stepID string, handbackID int64, handbackStatus, summary string, body any) error {
	status := StepHandedBack
	reason := ""
	if handbackStatus == "failed" {
		status, reason = StepFailed, "the Warden handed back failed"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := s.stamp()
	res, err := tx.Exec("UPDATE steps SET status = ?, handback_id = ?, reason = ?, finished_at = ? WHERE job_id = ? AND step = ? AND status = ?",
		status, handbackID, reason, at, jobID, stepID, StepRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("step %q of job %q is not running: %w", stepID, jobID, ErrConflict)
	}
	if err := appendEvent(tx, at, jobID, "step."+status, map[string]any{"step": stepID, "handback": handbackID}); err != nil {
		return err
	}
	if err := addInbox(tx, at, InboxReport, "step_handback", jobID, summary, map[string]any{"step": stepID, "handback_id": handbackID, "status": handbackStatus, "body": body}, 0); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notifyInbox()
	return nil
}

// FailStep ends a running step whose Warden could not finish, and tells the
// Archwright's inbox. It is a no-op for a step that is no longer running.
func (s *Store) FailStep(jobID, stepID, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := s.stamp()
	res, err := tx.Exec("UPDATE steps SET status = ?, reason = ?, finished_at = ? WHERE job_id = ? AND step = ? AND status = ?", StepFailed, reason, at, jobID, stepID, StepRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if err := appendEvent(tx, at, jobID, "step.failed", map[string]string{"step": stepID, "reason": reason}); err != nil {
		return err
	}
	if err := addInbox(tx, at, InboxReport, "step_failed", jobID, fmt.Sprintf("Step %s failed: %s", stepID, reason), map[string]any{"step": stepID, "reason": reason}, 0); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notifyInbox()
	return nil
}
