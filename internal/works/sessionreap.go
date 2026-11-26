package works

import (
	"errors"
	"log"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

// JobSessions is a set of open Sessions the Works keeps for its Jobs: each
// stays open after a handback until its Job leaves active work (ADR 0029).
type JobSessions interface {
	// OpenJobs lists the Jobs that hold a Session.
	OpenJobs() []string
	// CloseJob ends the Job's Sessions that are idle and removes their panes;
	// it returns how many it had to leave because they are not idle.
	CloseJob(job string) (remaining int)
}

// DefaultSessionRetention is how long a Job's Sessions stay open after the Job
// leaves active work, so the developer can still read the panes.
const DefaultSessionRetention = 30 * time.Second

// sessionReapEvery is how often the Works looks at which Jobs have left active work.
const sessionReapEvery = time.Second

// UseSessions makes the Works close the Sessions of each Job that leaves
// active work, after retention (zero means DefaultSessionRetention).
func (r *Runs) UseSessions(js JobSessions, retention time.Duration) {
	if retention <= 0 {
		retention = DefaultSessionRetention
	}
	r.mu.Lock()
	r.sessions, r.sessionRetention = js, retention
	r.mu.Unlock()
}

// jobActive says whether a Job is in active work: its PR is not yet opened, it
// is not closed (accepted or discarded, which is how a cancelled or failed Job
// ends) or purged, and it is not complete (every Plan step handed back with no
// PR, so it waits on the developer and not on any agent).
func (r *Runs) jobActive(j store.Job) (bool, error) {
	if j.Outcome != "" || j.PR != "" || j.PurgedAt != "" {
		return false, nil
	}
	b, err := boardJob(r.st, j)
	return b.State != BoardComplete, err
}

// reapSessions closes the Sessions of the Jobs that left active work at least
// retention ago. left remembers when each Job was first seen to have left.
func (r *Runs) reapSessions(left map[string]time.Time, now time.Time) {
	r.mu.Lock()
	js, retention := r.sessions, r.sessionRetention
	r.mu.Unlock()
	if js == nil {
		return
	}
	open := map[string]bool{}
	for _, id := range js.OpenJobs() {
		open[id] = true
		job, err := r.st.GetJob(id)
		if err == nil {
			var active bool
			if active, err = r.jobActive(job); err == nil && active {
				delete(left, id) // back in active work, or never left
				continue
			}
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("session %s: reading its Job: %v", id, err)
			continue
		}
		since, ok := left[id]
		if !ok {
			left[id], since = now, now
		}
		if now.Sub(since) >= retention && js.CloseJob(id) == 0 {
			delete(left, id)
		}
	}
	for id := range left {
		if !open[id] {
			delete(left, id)
		}
	}
}

// reapLoop runs reapSessions until the Works stops.
func (r *Runs) reapLoop() {
	defer r.wg.Done()
	tick := time.NewTicker(sessionReapEvery)
	defer tick.Stop()
	left := map[string]time.Time{}
	for {
		select {
		case <-r.bg.Done():
			return
		case now := <-tick.C:
			r.reapSessions(left, now)
		}
	}
}
