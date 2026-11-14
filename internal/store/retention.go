package store

import (
	"errors"
	"fmt"
	"time"
)

// DefaultRetention is how long after a Job ends its raw run content is kept (ADR 0017).
const DefaultRetention = 30 * 24 * time.Hour

// PrunedJob is one Job whose raw run content was, or with a dry run would be, pruned.
type PrunedJob struct {
	JobID      string    `json:"job_id"`
	Title      string    `json:"title"`
	Outcome    string    `json:"outcome"`
	EndedAt    time.Time `json:"ended_at"`
	Dispatches int       `json:"dispatches"` // briefs as Wrights saw them
	Bytes      int64     `json:"bytes"`      // raw content removed
}

// PruneReport says what retention did.
type PruneReport struct {
	DryRun  bool        `json:"dry_run"`
	Cutoff  time.Time   `json:"cutoff"` // Jobs that ended before this are due
	Pruned  []PrunedJob `json:"pruned"`
	Active  []string    `json:"skipped_active,omitempty"` // due Jobs left alone because a run is active
	Compact bool        `json:"compacted"`
}

// PruneRaw removes the raw run content of every Job that ended more than
// retention ago, leaving the marker (purged_at and a job.purged event) and
// every durable record (ADR 0017). A Job with an active run is never touched.
// A dry run reports the same Jobs and changes nothing. After a real prune the
// database is compacted.
func (s *Store) PruneRaw(retention time.Duration, dryRun bool) (PruneReport, error) {
	cutoff := s.now().UTC().Add(-retention)
	rep := PruneReport{DryRun: dryRun, Cutoff: cutoff, Pruned: []PrunedJob{}}
	rows, err := s.db.Query(`SELECT id, title, COALESCE(outcome,''), ended_at FROM jobs
		WHERE ended_at IS NOT NULL AND purged_at IS NULL ORDER BY ended_at, id`)
	if err != nil {
		return rep, err
	}
	var due []PrunedJob
	for rows.Next() {
		var p PrunedJob
		var at string
		if err := rows.Scan(&p.JobID, &p.Title, &p.Outcome, &at); err != nil {
			rows.Close()
			return rep, err
		}
		t, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			rows.Close()
			return rep, fmt.Errorf("job %s: ended_at %q: %w", p.JobID, at, err)
		}
		if t.Before(cutoff) {
			p.EndedAt = t
			due = append(due, p)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return rep, err
	}
	rows.Close()
	for _, p := range due {
		active, err := hasActiveRun(s.db, p.JobID)
		if err != nil {
			return rep, err
		}
		if active {
			rep.Active = append(rep.Active, p.JobID)
			continue
		}
		if err := s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(LENGTH(rendered)),0) FROM dispatches WHERE job_id = ? AND rendered <> ''", p.JobID).Scan(&p.Dispatches, &p.Bytes); err != nil {
			return rep, err
		}
		if !dryRun {
			if err := s.purgeJob(p.JobID, "retention"); err != nil {
				if errors.Is(err, ErrConflict) { // a run started since the check
					rep.Active = append(rep.Active, p.JobID)
					continue
				}
				return rep, err
			}
		}
		rep.Pruned = append(rep.Pruned, p)
	}
	if !dryRun && len(rep.Pruned) > 0 {
		if err := s.Compact(); err != nil {
			return rep, fmt.Errorf("compacting the database: %w", err)
		}
		rep.Compact = true
	}
	return rep, nil
}

// Compact reclaims the space freed by pruning.
func (s *Store) Compact() error {
	_, err := s.db.Exec("VACUUM")
	return err
}

// ActiveRuns counts what is running now: Wrights queued or running, Reviews
// and gate runs. An upgrade restart would interrupt them.
func (s *Store) ActiveRuns() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM dispatches WHERE status IN (?, ?)) +
		(SELECT COUNT(*) FROM reviews WHERE status = 'running') +
		(SELECT COUNT(*) FROM gate_runs WHERE status = 'running')`, DispatchQueued, DispatchRunning).Scan(&n)
	return n, err
}
