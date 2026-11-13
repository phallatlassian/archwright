package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound means the requested Job or Plan does not exist.
var ErrNotFound = errors.New("not found")

// Job is one developer request.
type Job struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Site        string    `json:"site,omitempty"`
	Outcome     string    `json:"outcome,omitempty"` // accepted or discarded once closed
	CreatedAt   time.Time `json:"created_at"`
}

// Step is one node of a Plan: work for a Guild, blocked by other steps.
type Step struct {
	ID         string   `json:"id"`
	Guild      string   `json:"guild"`
	Title      string   `json:"title"`
	BlockedBy  []string `json:"blocked_by,omitempty"`
	Checkpoint bool     `json:"checkpoint,omitempty"` // a human checkpoint
}

// Plan is the task graph held for one Job.
type Plan struct {
	JobID     string    `json:"job_id"`
	Steps     []Step    `json:"steps"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339Nano) }

func newID(prefix string) string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

// CreateJob records a new Job and a job.created event.
func (s *Store) CreateJob(title, description string) (Job, error) {
	return s.createJob("", title, description)
}

func (s *Store) createJob(site, title, description string) (Job, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Job{}, errors.New("job title is required")
	}
	now := s.now().UTC()
	at := now.Format(time.RFC3339Nano)
	j := Job{ID: newID("job-"), Title: title, Description: description, Site: site, CreatedAt: now}
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO jobs (id, title, description, site, created_at) VALUES (?,?,?,NULLIF(?,''),?)", j.ID, j.Title, j.Description, j.Site, at); err != nil {
		return Job{}, err
	}
	if err := appendEvent(tx, at, j.ID, "job.created", map[string]string{"title": j.Title, "site": j.Site}); err != nil {
		return Job{}, err
	}
	return j, tx.Commit()
}

const jobCols = "id, title, description, COALESCE(site,''), COALESCE(outcome,''), created_at"

// GetJob reads one Job.
func (s *Store) GetJob(id string) (Job, error) {
	var j Job
	var at string
	err := s.db.QueryRow("SELECT "+jobCols+" FROM jobs WHERE id = ?", id).Scan(&j.ID, &j.Title, &j.Description, &j.Site, &j.Outcome, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("job %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Job{}, err
	}
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return j, nil
}

// ListJobs returns all Jobs, oldest first.
func (s *Store) ListJobs() ([]Job, error) {
	rows, err := s.db.Query("SELECT " + jobCols + " FROM jobs ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		var j Job
		var at string
		if err := rows.Scan(&j.ID, &j.Title, &j.Description, &j.Site, &j.Outcome, &at); err != nil {
			return nil, err
		}
		j.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// SetPlan validates and stores the Job's Plan, replacing any previous one.
func (s *Store) SetPlan(jobID string, steps []Step) (Plan, error) {
	if err := validateSteps(steps); err != nil {
		return Plan{}, err
	}
	if _, err := s.GetJob(jobID); err != nil {
		return Plan{}, err
	}
	raw, err := json.Marshal(steps)
	if err != nil {
		return Plan{}, err
	}
	at := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO plans (job_id, steps, updated_at) VALUES (?,?,?)
		ON CONFLICT(job_id) DO UPDATE SET steps = excluded.steps, updated_at = excluded.updated_at`, jobID, string(raw), at); err != nil {
		return Plan{}, err
	}
	if err := appendEvent(tx, at, jobID, "plan.set", map[string]int{"steps": len(steps)}); err != nil {
		return Plan{}, err
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, err
	}
	return s.GetPlan(jobID)
}

// GetPlan reads the Job's Plan.
func (s *Store) GetPlan(jobID string) (Plan, error) {
	p := Plan{JobID: jobID}
	var raw, at string
	err := s.db.QueryRow("SELECT steps, updated_at FROM plans WHERE job_id = ?", jobID).Scan(&raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, fmt.Errorf("plan for job %q: %w", jobID, ErrNotFound)
	}
	if err != nil {
		return Plan{}, err
	}
	if err := json.Unmarshal([]byte(raw), &p.Steps); err != nil {
		return Plan{}, err
	}
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return p, nil
}

func validateSteps(steps []Step) error {
	ids := map[string]*Step{}
	for i := range steps {
		st := &steps[i]
		if st.ID == "" || st.Guild == "" || st.Title == "" {
			return fmt.Errorf("step %d: id, guild and title are required", i+1)
		}
		if ids[st.ID] != nil {
			return fmt.Errorf("duplicate step id %q", st.ID)
		}
		ids[st.ID] = st
	}
	for _, st := range steps {
		for _, b := range st.BlockedBy {
			if ids[b] == nil {
				return fmt.Errorf("step %q is blocked by unknown step %q", st.ID, b)
			}
		}
	}
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("plan has a blocking cycle through step %q", id)
		case done:
			return nil
		}
		state[id] = visiting
		for _, b := range ids[id].BlockedBy {
			if err := visit(b); err != nil {
				return err
			}
		}
		state[id] = done
		return nil
	}
	for _, st := range steps {
		if err := visit(st.ID); err != nil {
			return err
		}
	}
	return nil
}

// Event is one row of the append-only events table.
type Event struct {
	ID      int64           `json:"id"`
	At      time.Time       `json:"at"`
	JobID   string          `json:"job_id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func appendEvent(tx execer, at, jobID, typ string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO events (at, job_id, type, payload) VALUES (?,?,?,?)", at, jobID, typ, string(raw))
	return err
}

// Events returns a Job's events in order.
func (s *Store) Events(jobID string) ([]Event, error) {
	rows, err := s.db.Query("SELECT id, at, COALESCE(job_id,''), type, payload FROM events WHERE job_id = ? ORDER BY id", jobID)
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
