package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Review statuses.
const (
	ReviewRunning  = "running"
	ReviewReported = "reported"
	ReviewErrored  = "errored" // the run ended without a verdict; says nothing about the work
)

// Verdicts: each axis, and the overall.
const (
	VerdictPass         = "pass"
	VerdictFail         = "fail"
	VerdictNotEvaluated = "not_evaluated"
)

// Where a Review's Spec axis took its spec from (ADR 0012).
const (
	SpecWrit           = "writ"
	SpecPlanAndRequest = "plan-and-request" // the Plan step briefs and the Job's original request
	SpecNone           = "none"
)

// Finding severities.
const (
	SeverityBlocker = "blocker"
	SeverityMajor   = "major"
	SeverityMinor   = "minor"
)

// Finding is one thing a Review found, tied to a line and a reason.
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
}

// Axis is a Review's verdict on one axis.
type Axis struct {
	Verdict  string    `json:"verdict"`
	Reason   string    `json:"reason,omitempty"` // why not evaluated, or the verdict in a line
	Findings []Finding `json:"findings"`
}

// Review is one read-only inspection of a Job's integration branch by a
// fresh Warden (ADR 0012). Seq numbers a Job's reviews from 1.
type Review struct {
	ID         string `json:"id"`
	JobID      string `json:"job_id"`
	Seq        int    `json:"seq"`
	HandbackID int64  `json:"handback_id"` // the handback it reviews
	Branch     string `json:"branch"`
	SHA        string `json:"sha"` // the commit it reviewed
	RunID      string `json:"run_id,omitempty"`
	Status     string `json:"status"`
	SpecBasis  string `json:"spec_basis"` // which spec the Spec axis used
	Brief      string `json:"brief"`      // what the Warden was given; none of Engineering's context
	Overall    string `json:"overall,omitempty"`
	Standards  *Axis  `json:"standards,omitempty"`
	Spec       *Axis  `json:"spec,omitempty"`
	Reason     string `json:"reason,omitempty"` // why an errored review errored
	// ReworkConstraints are the findings handed to Engineering as constraints,
	// once this review's one automatic rework has been issued.
	ReworkConstraints []string   `json:"rework_constraints,omitempty"`
	ReworkedAt        *time.Time `json:"reworked_at,omitempty"`
	reworkFloor       int64
	CreatedAt         time.Time  `json:"created_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
}

// ReworkFloor is the id of the latest handback when the rework was issued;
// the next review needs a handback after it.
func (r Review) ReworkFloor() int64 { return r.reworkFloor }

const reviewCols = "id, job_id, seq, handback_id, branch, sha, run_id, status, spec_basis, brief, overall, standards, spec, reason, rework_constraints, rework_floor, COALESCE(reworked_at,''), created_at, COALESCE(finished_at,'')"

func scanReview(r scanner) (Review, error) {
	var v Review
	var std, spec, cons, reworked, created, finished string
	if err := r.Scan(&v.ID, &v.JobID, &v.Seq, &v.HandbackID, &v.Branch, &v.SHA, &v.RunID, &v.Status, &v.SpecBasis, &v.Brief, &v.Overall, &std, &spec, &v.Reason, &cons, &v.reworkFloor, &reworked, &created, &finished); err != nil {
		return Review{}, err
	}
	if std != "" {
		v.Standards = &Axis{}
		if err := json.Unmarshal([]byte(std), v.Standards); err != nil {
			return Review{}, err
		}
	}
	if spec != "" {
		v.Spec = &Axis{}
		if err := json.Unmarshal([]byte(spec), v.Spec); err != nil {
			return Review{}, err
		}
	}
	if cons != "" {
		if err := json.Unmarshal([]byte(cons), &v.ReworkConstraints); err != nil {
			return Review{}, err
		}
	}
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if reworked != "" {
		t, _ := time.Parse(time.RFC3339Nano, reworked)
		v.ReworkedAt = &t
	}
	if finished != "" {
		t, _ := time.Parse(time.RFC3339Nano, finished)
		v.FinishedAt = &t
	}
	return v, nil
}

// NewReview opens a Job's next review. Only one review of a Job runs at a time.
func (s *Store) NewReview(jobID string, handbackID int64, branch, sha, specBasis string, render func(Review) string) (Review, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Review{}, err
	}
	defer tx.Rollback()
	var running int
	if err := tx.QueryRow("SELECT COUNT(*) FROM reviews WHERE job_id = ? AND status = ?", jobID, ReviewRunning).Scan(&running); err != nil {
		return Review{}, err
	}
	if running > 0 {
		return Review{}, fmt.Errorf("job %q already has a review running: %w", jobID, ErrConflict)
	}
	var seq int
	if err := tx.QueryRow("SELECT COALESCE(MAX(seq),0)+1 FROM reviews WHERE job_id = ?", jobID).Scan(&seq); err != nil {
		return Review{}, err
	}
	at := s.stamp()
	if _, err := tx.Exec("UPDATE jobs SET pr_go_ahead_at = NULL WHERE id = ?", jobID); err != nil {
		return Review{}, err
	}
	v := Review{ID: newID("rev-"), JobID: jobID, Seq: seq, HandbackID: handbackID, Branch: branch, SHA: sha, Status: ReviewRunning, SpecBasis: specBasis}
	v.Brief = render(v)
	if _, err := tx.Exec("INSERT INTO reviews (id, job_id, seq, handback_id, branch, sha, status, spec_basis, brief, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)",
		v.ID, jobID, seq, handbackID, branch, sha, v.Status, specBasis, v.Brief, at); err != nil {
		return Review{}, err
	}
	if err := appendEvent(tx, at, jobID, "review.started", map[string]any{"review": v.ID, "seq": seq, "sha": sha, "spec_basis": specBasis}); err != nil {
		return Review{}, err
	}
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return v, tx.Commit()
}

// AttachReviewRun records which run carries out a review.
func (s *Store) AttachReviewRun(id, runID string) error {
	_, err := s.db.Exec("UPDATE reviews SET run_id = ? WHERE id = ?", runID, id)
	return err
}

// GetReview reads one review.
func (s *Store) GetReview(id string) (Review, error) {
	v, err := scanReview(s.db.QueryRow("SELECT "+reviewCols+" FROM reviews WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Review{}, fmt.Errorf("review %q: %w", id, ErrNotFound)
	}
	return v, err
}

// Reviews lists a Job's reviews, oldest first.
func (s *Store) Reviews(jobID string) ([]Review, error) {
	rows, err := s.db.Query("SELECT "+reviewCols+" FROM reviews WHERE job_id = ? ORDER BY seq", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Review{}
	for rows.Next() {
		v, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// LatestReview reads a Job's most recent review.
func (s *Store) LatestReview(jobID string) (Review, error) {
	v, err := scanReview(s.db.QueryRow("SELECT "+reviewCols+" FROM reviews WHERE job_id = ? ORDER BY seq DESC LIMIT 1", jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Review{}, fmt.Errorf("review of job %q: %w", jobID, ErrNotFound)
	}
	return v, err
}

// FinishReview records a running review's verdicts once, and its overall.
func (s *Store) FinishReview(id string, standards, spec Axis, overall string) (Review, error) {
	std, _ := json.Marshal(standards)
	sp, _ := json.Marshal(spec)
	tx, err := s.db.Begin()
	if err != nil {
		return Review{}, err
	}
	defer tx.Rollback()
	at := s.stamp()
	res, err := tx.Exec("UPDATE reviews SET status = ?, overall = ?, standards = ?, spec = ?, finished_at = ? WHERE id = ? AND status = ?",
		ReviewReported, overall, string(std), string(sp), at, id, ReviewRunning)
	if err != nil {
		return Review{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Review{}, fmt.Errorf("review %q is not running: %w", id, ErrConflict)
	}
	v, err := scanReview(tx.QueryRow("SELECT "+reviewCols+" FROM reviews WHERE id = ?", id))
	if err != nil {
		return Review{}, err
	}
	if err := appendEvent(tx, at, v.JobID, "review.reported", map[string]any{"review": id, "overall": overall, "standards": standards.Verdict, "spec": spec.Verdict}); err != nil {
		return Review{}, err
	}
	return v, tx.Commit()
}

// ErrorReview ends a running review whose run never reported a verdict.
func (s *Store) ErrorReview(id, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var jobID string
	if err := tx.QueryRow("SELECT job_id FROM reviews WHERE id = ?", id).Scan(&jobID); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("review %q: %w", id, ErrNotFound)
	} else if err != nil {
		return err
	}
	at := s.stamp()
	res, err := tx.Exec("UPDATE reviews SET status = ?, reason = ?, finished_at = ? WHERE id = ? AND status = ?", ReviewErrored, reason, at, id, ReviewRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // it reported in the meantime
	}
	if err := appendEvent(tx, at, jobID, "review.errored", map[string]string{"review": id, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

// ReworkRounds counts the automatic rework rounds a Job has used.
func (s *Store) ReworkRounds(jobID string) (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM reviews WHERE job_id = ? AND reworked_at IS NOT NULL", jobID).Scan(&n)
	return n, err
}

// IssueRework records a Job's one automatic rework round, which answers the
// latest review's fail with constraints for Engineering. floor is the latest
// handback now: the re-review needs a handback after it.
func (s *Store) IssueRework(jobID string, constraints []string, floor int64) (Review, error) {
	cons, _ := json.Marshal(constraints)
	tx, err := s.db.Begin()
	if err != nil {
		return Review{}, err
	}
	defer tx.Rollback()
	last, err := scanReview(tx.QueryRow("SELECT "+reviewCols+" FROM reviews WHERE job_id = ? ORDER BY seq DESC LIMIT 1", jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Review{}, fmt.Errorf("job %q has no review to rework after: %w", jobID, ErrConflict)
	}
	if err != nil {
		return Review{}, err
	}
	if last.Status != ReviewReported || last.Overall != VerdictFail {
		return Review{}, fmt.Errorf("the latest review of job %q is not a fail, so there is nothing to rework: %w", jobID, ErrConflict)
	}
	var used int
	if err := tx.QueryRow("SELECT COUNT(*) FROM reviews WHERE job_id = ? AND reworked_at IS NOT NULL", jobID).Scan(&used); err != nil {
		return Review{}, err
	}
	if used > 0 {
		return Review{}, fmt.Errorf("job %q has used its one automatic rework round: %w", jobID, ErrConflict)
	}
	at := s.stamp()
	if _, err := tx.Exec("UPDATE reviews SET rework_constraints = ?, rework_floor = ?, reworked_at = ? WHERE id = ?", string(cons), floor, at, last.ID); err != nil {
		return Review{}, err
	}
	if err := appendEvent(tx, at, jobID, "review.rework", map[string]any{"review": last.ID, "constraints": len(constraints)}); err != nil {
		return Review{}, err
	}
	last.ReworkConstraints, last.reworkFloor = constraints, floor
	t, _ := time.Parse(time.RFC3339Nano, at)
	last.ReworkedAt = &t
	return last, tx.Commit()
}

// ParkJob parks a Job for the Archwright and files an item in the Needs-you
// inbox, in one step so a parked Job is never missing from it.
func (s *Store) ParkJob(jobID, reason string, item NewNeedsYou) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := s.stamp()
	if _, err := tx.Exec("UPDATE jobs SET parked_reason = ? WHERE id = ?", reason, jobID); err != nil {
		return err
	}
	if err := appendEvent(tx, at, jobID, "job.parked", map[string]string{"reason": reason}); err != nil {
		return err
	}
	if _, err := addNeedsYou(tx, at, item); err != nil {
		return err
	}
	return tx.Commit()
}

// SetWrit links the writ that authorised a Job's work.
func (s *Store) SetWrit(jobID, writ string) (Job, error) {
	writ = strings.TrimSpace(writ)
	if writ == "" {
		return Job{}, errors.New("writ is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE jobs SET writ = ? WHERE id = ?", writ, jobID)
	if err != nil {
		return Job{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Job{}, fmt.Errorf("job %q: %w", jobID, ErrNotFound)
	}
	if err := appendEvent(tx, s.stamp(), jobID, "job.writ_linked", map[string]int{"bytes": len(writ)}); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.GetJob(jobID)
}

// GrantGoAhead records the Archwright's PR go-ahead for a Job, which holds
// only while the Job's latest review is an overall pass of reviewID. The
// check and the write share one transaction.
func (s *Store) GrantGoAhead(jobID, reviewID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var parked, outcome string
	if err := tx.QueryRow("SELECT COALESCE(parked_reason,''), COALESCE(outcome,'') FROM jobs WHERE id = ?", jobID).Scan(&parked, &outcome); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("job %q: %w", jobID, ErrNotFound)
	} else if err != nil {
		return err
	}
	if parked != "" || outcome != "" {
		return fmt.Errorf("job %q is parked or closed: %w", jobID, ErrConflict)
	}
	last, err := scanReview(tx.QueryRow("SELECT "+reviewCols+" FROM reviews WHERE job_id = ? ORDER BY seq DESC LIMIT 1", jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("job %q has not been reviewed, and the PR go-ahead needs an overall pass: %w", jobID, ErrConflict)
	}
	if err != nil {
		return err
	}
	if last.ID != reviewID || last.Status != ReviewReported || last.Overall != VerdictPass {
		return fmt.Errorf("the latest review of job %q is %s, and the PR go-ahead needs an overall pass: %w", jobID, describeReview(last), ErrConflict)
	}
	at := s.stamp()
	if _, err := tx.Exec("UPDATE jobs SET pr_go_ahead_at = ? WHERE id = ?", at, jobID); err != nil {
		return err
	}
	if err := appendEvent(tx, at, jobID, "pr.go_ahead", map[string]string{"review": reviewID}); err != nil {
		return err
	}
	return tx.Commit()
}

func describeReview(v Review) string {
	switch v.Status {
	case ReviewReported:
		return "overall " + v.Overall
	default:
		return v.Status
	}
}

// InterruptReviews errors reviews a stopped Works left running, so a Job is
// never stuck waiting on a run that is gone.
func (s *Store) InterruptReviews() error {
	_, err := s.db.Exec("UPDATE reviews SET status = ?, reason = ?, finished_at = ? WHERE status = ?",
		ReviewErrored, "the Works stopped before the review reported", s.stamp(), ReviewRunning)
	return err
}
