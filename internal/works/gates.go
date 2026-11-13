package works

import (
	"errors"
	"log"
	"time"

	"github.com/phallatlassian/archwright/internal/gates"
	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/store"
)

var errNoSite = errors.New("the Job has no Site, so there are no gates to run")

// UseSites sets the Site manager that gates are checked out through. Without
// one, handbacks record that their gates were not run.
func (r *Runs) UseSites(sm *sites.Manager) {
	r.mu.Lock()
	r.sites = sm
	r.mu.Unlock()
}

// SetGateTimeout bounds each gate command; zero restores the default.
func (r *Runs) SetGateTimeout(d time.Duration) {
	r.mu.Lock()
	r.gateTimeout = d
	r.mu.Unlock()
}

// startGates re-runs the Job's Site gates on its handback branch in the
// background and records the result against the handback. The commands are
// the registry's as of now: neither the branch nor a later change to the
// Site alters what this run executes (ADR 0010). Gates can outlast a tool
// call, so a handback's gate run reads "running" until it finishes.
func (r *Runs) startGates(jobID string, handbackID int64) {
	run := store.GateRun{HandbackID: handbackID, JobID: jobID}
	notRun := func(reason string) {
		run.Status, run.Reason, run.Results = store.GateRunNotRun, reason, nil
		if err := r.st.FinishGateRun(run); err != nil {
			log.Printf("handback %d: recording gates: %v", handbackID, err)
		}
	}
	site, err := r.gateSite(jobID)
	if err != nil {
		notRun(err.Error())
		return
	}
	r.mu.Lock()
	sm, timeout, closed := r.sites, r.gateTimeout, r.bg.Err() != nil
	if !closed && sm != nil {
		r.wg.Add(1) // under mu, so Close cannot Wait between the check and the Add
	}
	r.mu.Unlock()
	if closed {
		return // left running; the next Works start marks it not run
	}
	if sm == nil {
		notRun("no Site manager is configured to check out the handback branch")
		return
	}
	go func() {
		defer r.wg.Done()
		co, err := sm.GateCheckout(jobID)
		if err != nil {
			notRun("checking out the handback branch: " + err.Error())
			return
		}
		run.Branch, run.SHA = co.Branch, co.SHA
		results := gates.Run(r.bg, co.Dir, site.Gates, timeout)
		if err := co.Release(); err != nil {
			log.Printf("handback %d: removing the gate checkout: %v", handbackID, err)
		}
		if r.bg.Err() != nil { // killed mid-run: the results say nothing
			notRun("the Works stopped before the gates finished")
			return
		}
		run.Results = results
		run.Status, run.Reason = gates.Summarize(results)
		if err := r.st.FinishGateRun(run); err != nil {
			log.Printf("handback %d: recording gates: %v", handbackID, err)
		}
	}()
}

// gateSite reads the Site whose registry gates apply to a Job.
func (r *Runs) gateSite(jobID string) (store.Site, error) {
	job, err := r.st.GetJob(jobID)
	if err != nil {
		return store.Site{}, err
	}
	if job.Site == "" {
		return store.Site{}, errNoSite
	}
	return r.st.GetSite(job.Site)
}
