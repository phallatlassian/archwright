package works

import (
	"context"
	"log"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

// pruneEvery is how often the running Works prunes (ADR 0017); it also prunes at start.
const pruneEvery = 24 * time.Hour

// SetRetention sets where the retention period comes from; it is read at each
// prune, so editing config.toml takes effect without a restart.
func (r *Runs) SetRetention(read func() (time.Duration, error)) {
	r.mu.Lock()
	r.retention = read
	r.mu.Unlock()
}

// Retention is the current retention period.
func (r *Runs) Retention() (time.Duration, error) {
	r.mu.Lock()
	read := r.retention
	r.mu.Unlock()
	if read == nil {
		return store.DefaultRetention, nil
	}
	return read()
}

// Prune removes raw run content from Jobs that ended longer ago than the
// retention period, or than days when it is positive (ADR 0017). A dry run
// changes nothing.
func (r *Runs) Prune(days int, dryRun bool) (store.PruneReport, error) {
	keep := time.Duration(days) * 24 * time.Hour
	if days <= 0 {
		var err error
		if keep, err = r.Retention(); err != nil {
			return store.PruneReport{}, err
		}
	}
	return r.st.PruneRaw(keep, dryRun)
}

// pruneLoop prunes now and then daily until ctx ends.
func (r *Runs) pruneLoop(ctx context.Context) {
	tick := time.NewTicker(pruneEvery)
	defer tick.Stop()
	for {
		rep, err := r.Prune(0, false)
		switch {
		case err != nil:
			log.Printf("retention: %v", err)
		case len(rep.Pruned) > 0:
			log.Printf("retention: pruned raw run content of %d Job(s)", len(rep.Pruned))
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// ActiveRuns counts the runs an upgrade restart would interrupt.
func (r *Runs) ActiveRuns() (int, error) { return r.st.ActiveRuns() }
