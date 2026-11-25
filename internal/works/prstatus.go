package works

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/phallatlassian/archwright/internal/forge"
	"github.com/phallatlassian/archwright/internal/store"
)

// PRStatus is the Works' own reading of a Job's recorded pull request, for the
// dashboard (ADR 0028). It never depends on an agent to report it: the Works
// asks the Forge, as the developer, and keeps the last answer. It carries no
// third-party text (no title or body).
type PRStatus struct {
	URL       string        `json:"url"`
	Number    int           `json:"number,omitempty"`
	State     string        `json:"state,omitempty"` // OPEN, CLOSED or MERGED
	Draft     bool          `json:"draft,omitempty"`
	CI        string        `json:"ci,omitempty"` // passing, failing, pending or none
	Mergeable string        `json:"mergeable,omitempty"`
	Checks    []forge.Check `json:"checks,omitempty"`
	CheckedAt string        `json:"checked_at,omitempty"` // when it was last read; a failed read keeps the older answer and sets Error
	Error     string        `json:"error,omitempty"`      // why the last read failed
}

// Defaults of the PR reader: how often it looks for Jobs to read, and how old
// a reading may get before the Job's PR is read again.
const (
	prWatchEvery = 5 * time.Second
	prReadAge    = 30 * time.Second
)

// PRStatus returns the last reading of a Job's PR, if one was made.
func (r *Runs) PRStatus(jobID string) (PRStatus, bool) {
	r.prMu.Lock()
	defer r.prMu.Unlock()
	s, ok := r.prStates[jobID]
	return s, ok
}

// ciSummary folds the checks into one word: failing if any completed without
// success, pending if any is still running, passing if all passed, else none.
func ciSummary(checks []forge.Check) string {
	if len(checks) == 0 {
		return "none"
	}
	pending := false
	for _, ck := range checks {
		if ck.Status != "completed" {
			pending = true
			continue
		}
		switch ck.Conclusion {
		case "success", "neutral", "skipped":
		default:
			return "failing"
		}
	}
	if pending {
		return "pending"
	}
	return "passing"
}

// prWatched says whether the Works reads a Job's PR: one is recorded and the
// Job is still open. A PR that merged or closed is read no more.
func prWatched(j store.Job) bool { return j.PR != "" && j.Outcome == "" && j.PurgedAt == "" }

func prSettled(s PRStatus) bool { return s.Error == "" && (s.State == "MERGED" || s.State == "CLOSED") }

// watchPR reads a Job's PR now, in the background: record_pr calls it so the
// dashboard does not wait for the next pass.
func (r *Runs) watchPR(jobID string) {
	r.mu.Lock()
	closed := r.bg.Err() != nil
	if !closed {
		r.wg.Add(1) // under mu, so Close cannot Wait between the check and the Add
	}
	r.mu.Unlock()
	if closed {
		return
	}
	go func() {
		defer r.wg.Done()
		if job, err := r.st.GetJob(jobID); err == nil && prWatched(job) {
			r.readPR(job)
		}
	}()
}

// readPR reads one Job's PR and keeps the answer. Two reads of one Job never
// overlap. A fault is kept beside the last good reading and is never retried
// sooner than the next pass; it raises nothing for the developer, who sees it
// on the dashboard.
func (r *Runs) readPR(job store.Job) {
	r.prMu.Lock()
	if r.prBusy[job.ID] {
		r.prMu.Unlock()
		return
	}
	r.prBusy[job.ID] = true
	prev := r.prStates[job.ID]
	r.prMu.Unlock()
	defer func() {
		r.prMu.Lock()
		delete(r.prBusy, job.ID)
		r.prMu.Unlock()
	}()

	pr, err := r.forgeOrDefault().ReadPR(r.bg, job.PR)
	at := time.Now().UTC().Format(time.RFC3339)
	next := PRStatus{URL: job.PR, CheckedAt: at}
	if err != nil {
		if r.bg.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		next = prev
		next.URL, next.CheckedAt = job.PR, at
		next.Error = err.Error()
		if prev.Error != next.Error {
			log.Printf("job %s: reading its pull request: %v", job.ID, err)
		}
	} else {
		next.Number, next.State, next.Draft, next.Mergeable = pr.Number, pr.State, pr.Draft, pr.Mergeable
		next.CI, next.Checks = ciSummary(pr.Checks), pr.Checks
	}
	r.prMu.Lock()
	r.prStates[job.ID] = next
	r.prMu.Unlock()
}

// refreshPRs reads the PR of every watched Job whose last reading is older
// than the read age, or that has none. A Job that is no longer watched keeps
// its last reading, so the dashboard still shows how its PR ended.
func (r *Runs) refreshPRs(now time.Time) {
	jobs, err := r.st.ListJobs()
	if err != nil {
		log.Printf("reading the Jobs for their pull requests: %v", err)
		return
	}
	for _, j := range jobs {
		if !prWatched(j) || r.bg.Err() != nil {
			continue
		}
		s, ok := r.PRStatus(j.ID)
		if ok && (prSettled(s) || now.Sub(parseStamp(s.CheckedAt)) < r.prAge) {
			continue
		}
		r.readPR(j)
	}
}

func parseStamp(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// prLoop keeps the readings fresh until the Works stops.
func (r *Runs) prLoop() {
	defer r.wg.Done()
	tick := time.NewTicker(prWatchEvery)
	defer tick.Stop()
	for {
		select {
		case <-r.bg.Done():
			return
		case now := <-tick.C:
			r.refreshPRs(now)
		}
	}
}
