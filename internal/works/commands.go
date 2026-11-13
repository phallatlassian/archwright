package works

import (
	"fmt"

	"github.com/phallatlassian/archwright/internal/store"
)

const cancelledByDeveloper = "cancelled by the developer"

// CancelDispatch cancels one Wright: a queued dispatch leaves the queue, a
// running one has its run stopped. Either ends as a failed result the Warden
// sees when it inspects the piece.
func (r *Runs) CancelDispatch(id string) (map[string]any, error) {
	d, err := r.st.GetDispatch(id)
	if err != nil {
		return nil, err
	}
	if d.Status != store.DispatchQueued && d.Status != store.DispatchRunning {
		return nil, fmt.Errorf("dispatch %q is %s, not queued or running: %w", id, d.Status, store.ErrConflict)
	}
	r.mu.Lock()
	r.cancelled[id] = true
	stop := r.cancels[id]
	r.mu.Unlock()
	if stop != nil {
		stop()
	}
	if d.Status == store.DispatchQueued && r.sched.dequeue(id) {
		r.mu.Lock()
		delete(r.cancelled, id) // it never ran
		r.mu.Unlock()
		r.failDispatch(id, cancelledByDeveloper)
	}
	if err := r.st.AppendEvent(d.JobID, "wright.cancelled", map[string]string{"dispatch": id, "piece": d.Piece}); err != nil {
		return nil, err
	}
	return map[string]any{"dispatch_id": id, "job_id": d.JobID, "piece": d.Piece, "status": "cancelled"}, nil
}

// liveDispatches lists a Job's Wrights that are queued or running.
func (r *Runs) liveDispatches(jobID string) ([]store.Dispatch, error) {
	all, err := r.st.Dispatches(jobID)
	if err != nil {
		return nil, err
	}
	var live []store.Dispatch
	for _, d := range all {
		if d.Status == store.DispatchQueued || d.Status == store.DispatchRunning {
			live = append(live, d)
		}
	}
	return live, nil
}

// CancelJob stops a Job's Wrights, cancels its open asks and discards the Job.
func (r *Runs) CancelJob(id string) (map[string]any, error) {
	job, err := r.st.GetJob(id)
	if err != nil {
		return nil, err
	}
	if job.Outcome != "" {
		return nil, fmt.Errorf("job %q is already %s: %w", id, job.Outcome, store.ErrConflict)
	}
	live, err := r.liveDispatches(id)
	if err != nil {
		return nil, err
	}
	stopped := []string{}
	for _, d := range live {
		if _, err := r.CancelDispatch(d.ID); err != nil {
			return nil, err
		}
		stopped = append(stopped, d.ID)
	}
	for _, status := range []string{store.AskOpen, store.AskEscalated} {
		asks, err := r.st.Asks(store.AskFilter{Job: id, Status: status})
		if err != nil {
			return nil, err
		}
		if len(asks) > 0 { // cancelling one cancels every live ask of the Job
			if _, err := r.st.CancelAsk(asks[0].ID, AnsweredByDashboard); err != nil {
				return nil, err
			}
			break
		}
	}
	if err := r.discard(id); err != nil {
		return nil, fmt.Errorf("work stopped but the Job could not be closed (cancel it again): %w", err)
	}
	r.sched.setPaused(id, false)
	if err := r.st.AppendEvent(id, "job.cancelled", map[string]any{"dispatches": stopped}); err != nil {
		return nil, err
	}
	return map[string]any{"job_id": id, "outcome": "discarded", "cancelled_dispatches": stopped}, nil
}

// PauseJob keeps a Job from starting any more Wrights; those running finish.
func (r *Runs) PauseJob(id string) (store.Job, error) {
	if err := r.st.PauseJob(id); err != nil {
		return store.Job{}, err
	}
	r.sched.setPaused(id, true)
	return r.st.GetJob(id)
}

// ResumeJob lets a paused Job start Wrights again, queued ones first.
func (r *Runs) ResumeJob(id string) (store.Job, error) {
	if err := r.st.ResumeJob(id); err != nil {
		return store.Job{}, err
	}
	r.sched.setPaused(id, false)
	return r.st.GetJob(id)
}

// PurgeJob removes a Job's raw run content and keeps its summary (ADR 0017).
func (r *Runs) PurgeJob(id string) (store.Job, error) {
	if err := r.st.PurgeJob(id); err != nil {
		return store.Job{}, err
	}
	return r.st.GetJob(id)
}

// DeleteJob removes a Job entirely. An open Job is closed first, so its
// worktrees go, but one with Wrights still running must be cancelled first.
func (r *Runs) DeleteJob(id string) (map[string]any, error) {
	job, err := r.st.GetJob(id)
	if err != nil {
		return nil, err
	}
	live, err := r.liveDispatches(id)
	if err != nil {
		return nil, err
	}
	if len(live) > 0 {
		return nil, fmt.Errorf("job %q has active Wrights; cancel it first: %w", id, store.ErrConflict)
	}
	if err := r.discard(id); err != nil {
		return nil, err
	}
	if err := r.st.DeleteJob(id); err != nil {
		return nil, err
	}
	r.sched.setPaused(id, false)
	return map[string]any{"job_id": job.ID, "title": job.Title, "deleted": true}, nil
}
