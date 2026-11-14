package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Inbox item kinds. A command is a judgment the Archwright must act on; a
// report tells it what the Works already did for the developer (ADR 0007).
const (
	InboxCommand = "command"
	InboxReport  = "report"
)

// InboxItem is one entry in the Archwright's inbox.
type InboxItem struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"` // the dashboard command, e.g. instruct or cancel_job
	JobID     string          `json:"job_id,omitempty"`
	Summary   string          `json:"summary"`
	Payload   json.RawMessage `json:"payload"`
	EventID   int64           `json:"event_id"` // the command event that caused it
	CreatedAt time.Time       `json:"created_at"`
	ReadAt    *time.Time      `json:"read_at,omitempty"`
}

// RecordCommand appends the command event for a dashboard command, before
// anything acts on it, and returns the event's id.
func (s *Store) RecordCommand(name, jobID string, args json.RawMessage) (int64, error) {
	return appendEventID(s.db, s.stamp(), jobID, "command."+name, commandPayload(args))
}

func commandPayload(args json.RawMessage) map[string]any {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return map[string]any{"origin": "dashboard", "args": args}
}

// QueueCommand records a judgment command and files it in the Archwright's
// inbox in one step, so a recorded command is never missing from the inbox.
func (s *Store) QueueCommand(name, jobID, summary string, args json.RawMessage) (int64, error) {
	at := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := appendEventID(tx, at, jobID, "command."+name, commandPayload(args))
	if err != nil {
		return 0, err
	}
	if err := addInbox(tx, at, InboxCommand, name, jobID, summary, commandPayload(args), id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ReportCommand records that the Works executed a mechanical command and tells
// the Archwright's inbox. eventJob is the Job the executed event files under;
// a deleted Job has none left.
func (s *Store) ReportCommand(eventID int64, name, eventJob, jobID, summary string, result any) error {
	at := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	payload := map[string]any{"command": eventID, "name": name, "summary": summary, "result": result}
	if _, err := appendEventID(tx, at, eventJob, "command.executed", payload); err != nil {
		return err
	}
	if err := addInbox(tx, at, InboxReport, name, jobID, summary, map[string]any{"result": result}, eventID); err != nil {
		return err
	}
	return tx.Commit()
}

// FailCommand records that a mechanical command was refused or failed.
func (s *Store) FailCommand(eventID int64, name, jobID string, cause error) error {
	_, err := appendEventID(s.db, s.stamp(), jobID, "command.failed", map[string]any{"command": eventID, "name": name, "error": cause.Error()})
	return err
}

func addInbox(tx execer, at, kind, name, jobID, summary string, payload any, eventID int64) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO inbox (kind, name, job_id, summary, payload, event_id, created_at) VALUES (?,?,?,?,?,?,?)", kind, name, jobID, summary, string(raw), eventID, at)
	return err
}

const inboxCols = "id, kind, name, job_id, summary, payload, event_id, created_at, read_at"

func scanInbox(rows *sql.Rows) ([]InboxItem, error) {
	defer rows.Close()
	items := []InboxItem{}
	for rows.Next() {
		var it InboxItem
		var payload, at string
		var read sql.NullString
		if err := rows.Scan(&it.ID, &it.Kind, &it.Name, &it.JobID, &it.Summary, &payload, &it.EventID, &at, &read); err != nil {
			return nil, err
		}
		it.Payload = json.RawMessage(payload)
		it.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		if read.Valid {
			t, _ := time.Parse(time.RFC3339Nano, read.String)
			it.ReadAt = &t
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// Inbox lists the Archwright's inbox, oldest first, without marking anything read.
func (s *Store) Inbox(unreadOnly bool) ([]InboxItem, error) {
	q := "SELECT " + inboxCols + " FROM inbox"
	if unreadOnly {
		q += " WHERE read_at IS NULL"
	}
	rows, err := s.db.Query(q + " ORDER BY id")
	if err != nil {
		return nil, err
	}
	return scanInbox(rows)
}

// ReadInbox returns the unread items, oldest first, and marks them read in
// the same transaction: each item is delivered to the Archwright once.
func (s *Store) ReadInbox() ([]InboxItem, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT " + inboxCols + " FROM inbox WHERE read_at IS NULL ORDER BY id")
	if err != nil {
		return nil, err
	}
	items, err := scanInbox(rows)
	if err != nil {
		return nil, err
	}
	if len(items) > 0 {
		if _, err := tx.Exec("UPDATE inbox SET read_at = ? WHERE read_at IS NULL AND id <= ?", s.stamp(), items[len(items)-1].ID); err != nil {
			return nil, err
		}
	}
	return items, tx.Commit()
}

// AppendEvent records an event for a Job.
func (s *Store) AppendEvent(jobID, typ string, payload any) error {
	return appendEvent(s.db, s.stamp(), jobID, typ, payload)
}

// EventsAfter returns up to limit events with an id above the cursor, oldest
// first, optionally only those of one Job. An id is never reused, so a reader
// that resumes from the last id it saw misses nothing.
func (s *Store) EventsAfter(after int64, jobID string, limit int) ([]Event, error) {
	q, args := "SELECT id, at, COALESCE(job_id,''), type, payload FROM events WHERE id > ?", []any{after}
	if jobID != "" {
		q += " AND job_id = ?"
		args = append(args, jobID)
	}
	rows, err := s.db.Query(q+" ORDER BY id LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evs := []Event{}
	for rows.Next() {
		var e Event
		var at, payload string
		if err := rows.Scan(&e.ID, &at, &e.JobID, &e.Type, &payload); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		e.Payload = json.RawMessage(payload)
		evs = append(evs, e)
	}
	return evs, rows.Err()
}

// PauseJob stops new Wrights starting for an open Job.
func (s *Store) PauseJob(id string) error { return s.setPaused(id, true) }

// ResumeJob lets a paused Job start Wrights again.
func (s *Store) ResumeJob(id string) error { return s.setPaused(id, false) }

func (s *Store) setPaused(id string, pause bool) error {
	j, err := s.GetJob(id)
	if err != nil {
		return err
	}
	if j.Outcome != "" {
		return fmt.Errorf("job %q is already %s: %w", id, j.Outcome, ErrConflict)
	}
	if pause == (j.PausedAt != "") {
		state := "not paused"
		if pause {
			state = "already paused"
		}
		return fmt.Errorf("job %q is %s: %w", id, state, ErrConflict)
	}
	at, typ, set := s.stamp(), "job.paused", any(nil)
	if pause {
		set = at
	} else {
		typ = "job.resumed"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE jobs SET paused_at = ? WHERE id = ?", set, id); err != nil {
		return err
	}
	if err := appendEvent(tx, at, id, typ, map[string]string{}); err != nil {
		return err
	}
	return tx.Commit()
}

// PausedJobs lists the open Jobs the developer has paused.
func (s *Store) PausedJobs() ([]string, error) {
	rows, err := s.db.Query("SELECT id FROM jobs WHERE paused_at IS NOT NULL AND outcome IS NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) hasLiveDispatch(q queryer, jobID string) (bool, error) {
	var n int
	err := q.QueryRow("SELECT COUNT(*) FROM dispatches WHERE job_id = ? AND status IN (?, ?)", jobID, DispatchQueued, DispatchRunning).Scan(&n)
	return n > 0, err
}

// hasActiveRun says whether anything is running for the Job: a Wright queued
// or running, a Review, or a gate run.
func hasActiveRun(q queryer, jobID string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT
		(SELECT COUNT(*) FROM dispatches WHERE job_id = ?1 AND status IN (?2, ?3)) +
		(SELECT COUNT(*) FROM reviews WHERE job_id = ?1 AND status = 'running') +
		(SELECT COUNT(*) FROM gate_runs WHERE job_id = ?1 AND status = 'running')`, jobID, DispatchQueued, DispatchRunning).Scan(&n)
	return n > 0, err
}

// PurgeJob removes a Job's raw run content (the briefs as Wrights saw them)
// at once and keeps its summary: Plan, pieces, handbacks, asks and events
// (ADR 0017). A Job with Wrights still queued or running is refused.
func (s *Store) PurgeJob(id string) error {
	return s.purgeJob(id, "developer")
}

// purgeJob is PurgeJob recording who asked: the developer, or retention.
func (s *Store) purgeJob(id, by string) error {
	j, err := s.GetJob(id)
	if err != nil {
		return err
	}
	if j.PurgedAt != "" {
		return fmt.Errorf("job %q was already purged: %w", id, ErrConflict)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if live, err := hasActiveRun(tx, id); err != nil {
		return err
	} else if live {
		return fmt.Errorf("job %q has active runs; cancel it first: %w", id, ErrConflict)
	}
	at := s.stamp()
	res, err := tx.Exec("UPDATE dispatches SET rendered = '' WHERE job_id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.Exec("UPDATE jobs SET purged_at = ? WHERE id = ?", at, id); err != nil {
		return err
	}
	if err := appendEvent(tx, at, id, "job.purged", map[string]any{"dispatches": n, "by": by}); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteJob removes a Job entirely: its rows and its events, leaving one
// job.deleted event that names it. The Job must be closed, so its worktrees
// are gone, and have no Wrights queued or running.
func (s *Store) DeleteJob(id string) error {
	j, err := s.GetJob(id)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if live, err := s.hasLiveDispatch(tx, id); err != nil {
		return err
	} else if live {
		return fmt.Errorf("job %q has active Wrights; cancel it first: %w", id, ErrConflict)
	}
	var wts int
	if err := tx.QueryRow("SELECT COUNT(*) FROM worktrees WHERE job_id = ? AND removed_at IS NULL", id).Scan(&wts); err != nil {
		return err
	}
	if wts > 0 {
		return fmt.Errorf("job %q still has %d worktrees; close it first: %w", id, wts, ErrConflict)
	}
	if _, err := tx.Exec("INSERT INTO event_deletions (job_id) VALUES (?)", id); err != nil {
		return err
	}
	for _, table := range []string{"gate_runs", "flags", "reviews", "handbacks", "asks", "dispatches", "pieces", "plans", "needs_you", "worktrees", "events"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE job_id = ?", id); err != nil {
			return fmt.Errorf("deleting %s: %w", table, err)
		}
	}
	if _, err := tx.Exec("DELETE FROM event_deletions WHERE job_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM jobs WHERE id = ?", id); err != nil {
		return err
	}
	if err := appendEvent(tx, s.stamp(), "", "job.deleted", map[string]string{"job": id, "title": j.Title}); err != nil {
		return err
	}
	return tx.Commit()
}
