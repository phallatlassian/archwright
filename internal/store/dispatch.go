package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Piece statuses.
const (
	PieceOpen     = "open"
	PieceAccepted = "accepted"
	PieceFailed   = "failed"
)

// Dispatch statuses.
const (
	DispatchRunning  = "running"  // the Wright's run has not reported
	DispatchReported = "reported" // a result is stored, awaiting the Warden's inspection
	DispatchRejected = "rejected" // the Warden sent the piece back, or it failed
)

// ErrConflict means the request is valid but the state does not allow it.
var ErrConflict = errors.New("conflict")

// Piece is one scoped unit of work within a Job, dispatched to Wrights in rounds.
type Piece struct {
	JobID     string `json:"job_id"`
	Piece     string `json:"piece"`
	Guild     string `json:"guild"`
	Status    string `json:"status"`
	Reworks   int    `json:"reworks"`
	Reason    string `json:"reason,omitempty"`    // why the piece failed
	Diagnosis string `json:"diagnosis,omitempty"` // the Warden's diagnosis of the failure
}

// Dispatch is one Wright run for a piece: round 0 is the first, each rework adds one.
type Dispatch struct {
	ID        string          `json:"id"`
	JobID     string          `json:"job_id"`
	Piece     string          `json:"piece"`
	Guild     string          `json:"guild"`
	Round     int             `json:"round"`
	RunID     string          `json:"run_id,omitempty"`
	Brief     json.RawMessage `json:"brief"`
	Rendered  string          `json:"rendered"` // the brief as the Wright saw it, Guild standards included
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// Flag is something a Warden raises for the Archwright alone.
type Flag struct {
	ID          int64     `json:"id"`
	JobID       string    `json:"job_id"`
	HandbackID  int64     `json:"handback_id"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	Evidence    string    `json:"evidence"`
	CreatedAt   time.Time `json:"created_at"`
}

// Handback is a Warden's inspected result for a Job. Flags are stored apart
// from it and are served to the Archwright only.
type Handback struct {
	ID        int64           `json:"id"`
	JobID     string          `json:"job_id"`
	Status    string          `json:"status"`
	Body      json.RawMessage `json:"body"`
	CreatedAt time.Time       `json:"created_at"`
}

const dispatchCols = "id, job_id, piece, guild, round, run_id, brief, rendered, status, COALESCE(result,''), created_at"

func scanDispatch(r scanner) (Dispatch, error) {
	var d Dispatch
	var brief, result, at string
	if err := r.Scan(&d.ID, &d.JobID, &d.Piece, &d.Guild, &d.Round, &d.RunID, &brief, &d.Rendered, &d.Status, &result, &at); err != nil {
		return Dispatch{}, err
	}
	d.Brief = json.RawMessage(brief)
	if result != "" {
		d.Result = json.RawMessage(result)
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return d, nil
}

// NewPiece opens a piece for a Job and records its first dispatch.
func (s *Store) NewPiece(jobID, guild, piece string, brief json.RawMessage, rendered string) (Dispatch, error) {
	if _, err := s.GetJob(jobID); err != nil {
		return Dispatch{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Dispatch{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO pieces (job_id, piece, guild, status) VALUES (?,?,?,?)", jobID, piece, guild, PieceOpen); err != nil {
		var exists int
		if tx.QueryRow("SELECT 1 FROM pieces WHERE job_id = ? AND piece = ?", jobID, piece).Scan(&exists) == nil {
			return Dispatch{}, fmt.Errorf("piece %q already dispatched; use rework_wright: %w", piece, ErrConflict)
		}
		return Dispatch{}, err
	}
	d, err := s.insertDispatch(tx, jobID, guild, piece, 0, brief, rendered)
	if err != nil {
		return Dispatch{}, err
	}
	return d, tx.Commit()
}

func (s *Store) insertDispatch(tx *sql.Tx, jobID, guild, piece string, round int, brief json.RawMessage, rendered string) (Dispatch, error) {
	at := s.stamp()
	d := Dispatch{ID: newID("dsp-"), JobID: jobID, Piece: piece, Guild: guild, Round: round, Brief: brief, Rendered: rendered, Status: DispatchRunning}
	if _, err := tx.Exec("INSERT INTO dispatches (id, job_id, piece, guild, round, brief, rendered, status, created_at) VALUES (?,?,?,?,?,?,?,?,?)",
		d.ID, jobID, piece, guild, round, string(brief), rendered, d.Status, at); err != nil {
		return Dispatch{}, err
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	err := appendEvent(tx, at, jobID, "wright.dispatched", map[string]any{"dispatch": d.ID, "piece": piece, "round": round})
	return d, err
}

// GetPiece reads one piece.
func (s *Store) GetPiece(jobID, piece string) (Piece, error) { return getPiece(s.db, jobID, piece) }

// queryer is a database handle or a transaction (the store has one connection, so reads inside a transaction must use it).
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func getPiece(q queryer, jobID, piece string) (Piece, error) {
	p := Piece{JobID: jobID, Piece: piece}
	err := q.QueryRow("SELECT guild, status, reworks, reason, diagnosis FROM pieces WHERE job_id = ? AND piece = ?", jobID, piece).
		Scan(&p.Guild, &p.Status, &p.Reworks, &p.Reason, &p.Diagnosis)
	if errors.Is(err, sql.ErrNoRows) {
		return Piece{}, fmt.Errorf("piece %q of job %q: %w", piece, jobID, ErrNotFound)
	}
	return p, err
}

// Pieces lists a Job's pieces in dispatch order.
func (s *Store) Pieces(jobID string) ([]Piece, error) {
	rows, err := s.db.Query("SELECT piece, guild, status, reworks, reason, diagnosis FROM pieces WHERE job_id = ? ORDER BY rowid", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ps := []Piece{}
	for rows.Next() {
		p := Piece{JobID: jobID}
		if err := rows.Scan(&p.Piece, &p.Guild, &p.Status, &p.Reworks, &p.Reason, &p.Diagnosis); err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

// GetDispatch reads one dispatch.
func (s *Store) GetDispatch(id string) (Dispatch, error) {
	d, err := scanDispatch(s.db.QueryRow("SELECT "+dispatchCols+" FROM dispatches WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Dispatch{}, fmt.Errorf("dispatch %q: %w", id, ErrNotFound)
	}
	return d, err
}

// LatestDispatch reads a piece's most recent dispatch.
func (s *Store) LatestDispatch(jobID, piece string) (Dispatch, error) {
	return latestDispatch(s.db, jobID, piece)
}

func latestDispatch(q queryer, jobID, piece string) (Dispatch, error) {
	d, err := scanDispatch(q.QueryRow("SELECT "+dispatchCols+" FROM dispatches WHERE job_id = ? AND piece = ? ORDER BY round DESC LIMIT 1", jobID, piece))
	if errors.Is(err, sql.ErrNoRows) {
		return Dispatch{}, fmt.Errorf("piece %q of job %q: %w", piece, jobID, ErrNotFound)
	}
	return d, err
}

// Dispatches lists a Job's dispatches in order.
func (s *Store) Dispatches(jobID string) ([]Dispatch, error) {
	rows, err := s.db.Query("SELECT "+dispatchCols+" FROM dispatches WHERE job_id = ? ORDER BY rowid", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ds := []Dispatch{}
	for rows.Next() {
		d, err := scanDispatch(rows)
		if err != nil {
			return nil, err
		}
		ds = append(ds, d)
	}
	return ds, rows.Err()
}

// AttachRun records which run carries out a dispatch.
func (s *Store) AttachRun(dispatchID, runID string) error {
	_, err := s.db.Exec("UPDATE dispatches SET run_id = ? WHERE id = ?", runID, dispatchID)
	return err
}

// RecordResult stores a Wright's result once; a dispatch that is not running refuses it.
func (s *Store) RecordResult(dispatchID string, result json.RawMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var jobID, piece string
	err = tx.QueryRow("SELECT job_id, piece FROM dispatches WHERE id = ?", dispatchID).Scan(&jobID, &piece)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("dispatch %q: %w", dispatchID, ErrNotFound)
	}
	if err != nil {
		return err
	}
	res, err := tx.Exec("UPDATE dispatches SET status = ?, result = ? WHERE id = ? AND status = ?", DispatchReported, string(result), dispatchID, DispatchRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("dispatch %q already reported: %w", dispatchID, ErrConflict)
	}
	if err := appendEvent(tx, s.stamp(), jobID, "wright.reported", map[string]any{"dispatch": dispatchID, "piece": piece}); err != nil {
		return err
	}
	return tx.Commit()
}

// Rework rejects a piece's last result. While the piece has rounds left it
// starts a fresh dispatch with the amended brief and returns it; once the
// limit is spent the piece fails with its reason and diagnosis, and no
// dispatch is returned.
func (s *Store) Rework(jobID, piece string, limit int, reason, diagnosis string, brief json.RawMessage, rendered string) (Piece, *Dispatch, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Piece{}, nil, err
	}
	defer tx.Rollback()
	p, err := getPiece(tx, jobID, piece)
	if err != nil {
		return Piece{}, nil, err
	}
	if p.Status != PieceOpen {
		return Piece{}, nil, fmt.Errorf("piece %q is %s: %w", piece, p.Status, ErrConflict)
	}
	last, err := latestDispatch(tx, jobID, piece)
	if err != nil {
		return Piece{}, nil, err
	}
	if last.Status != DispatchReported {
		return Piece{}, nil, fmt.Errorf("piece %q has no result to rework (dispatch is %s): %w", piece, last.Status, ErrConflict)
	}
	at := s.stamp()
	if _, err := tx.Exec("UPDATE dispatches SET status = ? WHERE id = ?", DispatchRejected, last.ID); err != nil {
		return Piece{}, nil, err
	}
	if p.Reworks >= limit {
		if _, err := tx.Exec("UPDATE pieces SET status = ?, reason = ?, diagnosis = ? WHERE job_id = ? AND piece = ?", PieceFailed, reason, diagnosis, jobID, piece); err != nil {
			return Piece{}, nil, err
		}
		if err := appendEvent(tx, at, jobID, "piece.failed", map[string]any{"piece": piece, "reworks": p.Reworks}); err != nil {
			return Piece{}, nil, err
		}
		p.Status, p.Reason, p.Diagnosis = PieceFailed, reason, diagnosis
		return p, nil, tx.Commit()
	}
	if _, err := tx.Exec("UPDATE pieces SET reworks = reworks + 1 WHERE job_id = ? AND piece = ?", jobID, piece); err != nil {
		return Piece{}, nil, err
	}
	p.Reworks++
	d, err := s.insertDispatch(tx, jobID, p.Guild, piece, p.Reworks, brief, rendered)
	if err != nil {
		return Piece{}, nil, err
	}
	return p, &d, tx.Commit()
}

// AcceptPiece accepts a piece whose latest dispatch reported done.
func (s *Store) AcceptPiece(jobID, piece string, doneResult func(json.RawMessage) bool) error {
	p, err := s.GetPiece(jobID, piece)
	if err != nil {
		return err
	}
	if p.Status != PieceOpen {
		return fmt.Errorf("piece %q is %s: %w", piece, p.Status, ErrConflict)
	}
	last, err := s.LatestDispatch(jobID, piece)
	if err != nil {
		return err
	}
	if last.Status != DispatchReported || !doneResult(last.Result) {
		return fmt.Errorf("piece %q has no done result to accept: %w", piece, ErrConflict)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE pieces SET status = ? WHERE job_id = ? AND piece = ?", PieceAccepted, jobID, piece); err != nil {
		return err
	}
	if err := appendEvent(tx, s.stamp(), jobID, "piece.accepted", map[string]string{"piece": piece}); err != nil {
		return err
	}
	return tx.Commit()
}

// NewFlag is a flag as a handback supplies it.
type NewFlag struct{ Kind, Description, Evidence string }

// SaveHandback stores a Warden's handback and its flags. A partial handback
// parks the Job with parkReason, keeping the accepted pieces as they are.
func (s *Store) SaveHandback(jobID, status string, body json.RawMessage, flags []NewFlag, parkReason string) (Handback, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Handback{}, err
	}
	defer tx.Rollback()
	at := s.stamp()
	res, err := tx.Exec("INSERT INTO handbacks (job_id, status, body, created_at) VALUES (?,?,?,?)", jobID, status, string(body), at)
	if err != nil {
		return Handback{}, err
	}
	id, _ := res.LastInsertId()
	for _, f := range flags {
		if _, err := tx.Exec("INSERT INTO flags (job_id, handback_id, kind, description, evidence, created_at) VALUES (?,?,?,?,?,?)", jobID, id, f.Kind, f.Description, f.Evidence, at); err != nil {
			return Handback{}, err
		}
	}
	if parkReason != "" {
		if _, err := tx.Exec("UPDATE jobs SET parked_reason = ? WHERE id = ?", parkReason, jobID); err != nil {
			return Handback{}, err
		}
	}
	if err := appendEvent(tx, at, jobID, "handback."+status, map[string]int{"flags": len(flags)}); err != nil {
		return Handback{}, err
	}
	if err := tx.Commit(); err != nil {
		return Handback{}, err
	}
	t, _ := time.Parse(time.RFC3339Nano, at)
	return Handback{ID: id, JobID: jobID, Status: status, Body: body, CreatedAt: t}, nil
}

// LatestHandback reads a Job's most recent handback.
func (s *Store) LatestHandback(jobID string) (Handback, error) {
	var h Handback
	var body, at string
	err := s.db.QueryRow("SELECT id, job_id, status, body, created_at FROM handbacks WHERE job_id = ? ORDER BY id DESC LIMIT 1", jobID).Scan(&h.ID, &h.JobID, &h.Status, &body, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Handback{}, fmt.Errorf("handback for job %q: %w", jobID, ErrNotFound)
	}
	if err != nil {
		return Handback{}, err
	}
	h.Body = json.RawMessage(body)
	h.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return h, nil
}

// Flags lists the flags a Job's Wardens have raised.
func (s *Store) Flags(jobID string) ([]Flag, error) {
	rows, err := s.db.Query("SELECT id, job_id, handback_id, kind, description, evidence, created_at FROM flags WHERE job_id = ? ORDER BY id", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fs := []Flag{}
	for rows.Next() {
		var f Flag
		var at string
		if err := rows.Scan(&f.ID, &f.JobID, &f.HandbackID, &f.Kind, &f.Description, &f.Evidence, &at); err != nil {
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		fs = append(fs, f)
	}
	return fs, rows.Err()
}
