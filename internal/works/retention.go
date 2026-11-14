package works

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/phallatlassian/archwright/internal/launch"
	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/store"
)

// pruneEvery is how often the running Works prunes (ADR 0017); it also prunes at start.
const pruneEvery = 24 * time.Hour

// ReadRetention returns how long after a Job ends its raw run content is
// kept: `retention_days = N` in config.toml, 30 days when the file or key is
// absent. N must be a whole number of days, 1 or more.
func ReadRetention(d paths.Dirs) (time.Duration, error) {
	f, err := os.Open(launch.ConfigFile(d))
	if os.IsNotExist(err) {
		return store.DefaultRetention, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	keep := store.DefaultRetention
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "retention_days" {
			continue
		}
		n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(v), `"'`))
		if err != nil || n < 1 {
			return 0, fmt.Errorf("%s: retention_days %q must be a whole number of days, 1 or more", launch.ConfigFile(d), strings.TrimSpace(v))
		}
		keep = time.Duration(n) * 24 * time.Hour
	}
	return keep, sc.Err()
}

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
