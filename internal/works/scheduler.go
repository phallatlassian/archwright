package works

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

// Limits caps concurrent Wright runs, centrally (ADR 0004, 0008). A Site's
// registered concurrency cap, when it has one, replaces PerSite for that Site.
type Limits struct {
	Total     int `json:"total"`      // runs overall
	PerSite   int `json:"per_site"`   // runs on one Site
	PerWarden int `json:"per_warden"` // Wrights of one Warden task
}

// DefaultLimits are the caps ADR 0008 sets.
var DefaultLimits = Limits{Total: 4, PerSite: 3, PerWarden: 3}

func (l Limits) withDefaults() Limits {
	if l.Total <= 0 {
		l.Total = DefaultLimits.Total
	}
	if l.PerSite <= 0 {
		l.PerSite = DefaultLimits.PerSite
	}
	if l.PerWarden <= 0 {
		l.PerWarden = DefaultLimits.PerWarden
	}
	return l
}

// QueueEntry is one dispatch as the scheduler shows it.
type QueueEntry struct {
	DispatchID string `json:"dispatch_id"`
	Job        string `json:"job"`
	Piece      string `json:"piece"`
	Guild      string `json:"guild"`
	Site       string `json:"site,omitempty"`
	Position   int    `json:"position,omitempty"` // 1 is next, for queued entries
	Waiting    string `json:"waiting,omitempty"`  // what a queued entry is waiting on
}

// QueueStatus is the visible state of the scheduler.
type QueueStatus struct {
	Limits  Limits       `json:"limits"`
	Running []QueueEntry `json:"running"`
	Queued  []QueueEntry `json:"queued"` // FIFO
	// Wardens are the running Warden runs of Plan steps. They are active runs
	// but sit outside the per-Site cap, which is about Wrights (ADR 0021).
	Wardens []WardenEntry `json:"wardens"`
}

// WardenEntry is one running Plan step's Warden as the queue shows it.
type WardenEntry struct {
	Job   string `json:"job"`
	Step  string `json:"step"`
	Guild string `json:"guild"`
}

type entry struct {
	d       store.Dispatch
	seq     int // arrival order; a requeued dispatch keeps its place
	site    string
	siteCap int // zero: use Limits.PerSite
	warden  string
}

// scheduler decides when a dispatch may start. Excess dispatches wait FIFO:
// each pass starts every queued dispatch that fits, oldest first, so a
// dispatch blocked by its own Site's cap does not hold up another Site's.
type scheduler struct {
	r *Runs

	mu      sync.Mutex
	limits  Limits
	now     func() time.Time
	seq     int
	running map[string]entry
	queue   []entry
	paused  map[string]bool // Jobs the developer paused: none of their queued dispatches start
	timer   *time.Timer
}

func newScheduler(r *Runs) *scheduler {
	return &scheduler{r: r, limits: DefaultLimits, now: time.Now, running: map[string]entry{}, paused: map[string]bool{}}
}

// LimitsFromEnv reads the caps from ARCHWRIGHT_MAX_RUNS, ARCHWRIGHT_MAX_RUNS_PER_SITE
// and ARCHWRIGHT_MAX_WRIGHTS_PER_WARDEN; unset or invalid ones keep their defaults.
func LimitsFromEnv(getenv func(string) string) Limits {
	n := func(k string) int {
		v, _ := strconv.Atoi(getenv(k))
		return max(v, 0)
	}
	return Limits{Total: n("ARCHWRIGHT_MAX_RUNS"), PerSite: n("ARCHWRIGHT_MAX_RUNS_PER_SITE"), PerWarden: n("ARCHWRIGHT_MAX_WRIGHTS_PER_WARDEN")}.withDefaults()
}

// Resume re-submits the dispatches left queued by a previous Works, oldest first.
func (r *Runs) Resume() error {
	paused, err := r.st.PausedJobs()
	if err != nil {
		return err
	}
	r.sched.mu.Lock()
	for _, id := range paused {
		r.sched.paused[id] = true
	}
	r.sched.mu.Unlock()
	queued, err := r.st.QueuedDispatches()
	if err != nil {
		return err
	}
	for _, d := range queued {
		if _, err := r.sched.submit(d); err != nil {
			log.Printf("dispatch %s: resuming: %v", d.ID, err)
		}
	}
	return r.resumeSteps()
}

// SetLimits replaces the caps; a zero field keeps its default.
func (r *Runs) SetLimits(l Limits) {
	r.sched.mu.Lock()
	r.sched.limits = l.withDefaults()
	errs := r.sched.pumpLocked()
	r.sched.mu.Unlock()
	r.sched.report(errs)
}

// Queue reports the scheduler's state: running and queued dispatches, in order.
func (r *Runs) Queue() QueueStatus {
	st := r.sched.status()
	st.Wardens = []WardenEntry{}
	running, err := r.st.RunningSteps()
	if err != nil {
		log.Printf("queue: listing the running steps: %v", err)
	}
	for _, sr := range running {
		st.Wardens = append(st.Wardens, WardenEntry{Job: sr.JobID, Step: sr.Step, Guild: sr.Guild})
	}
	return st
}

func (s *scheduler) entryFor(d store.Dispatch) (entry, error) {
	job, err := s.r.st.GetJob(d.JobID)
	if err != nil {
		return entry{}, err
	}
	e := entry{d: d, site: job.Site, warden: d.JobID + "/" + d.Guild}
	if job.Site != "" {
		if site, err := s.r.st.GetSite(job.Site); err == nil {
			e.siteCap = site.ConcurrencyCap
		}
	}
	return e, nil
}

// submit hands a queued dispatch to the scheduler. It returns the dispatch's
// place in the queue (zero if it started at once), or an error if it started
// at once and its run could not be launched; that failure is recorded on the
// dispatch.
func (s *scheduler) submit(d store.Dispatch) (int, error) {
	e, err := s.entryFor(d)
	if err != nil {
		s.r.failDispatch(d.ID, "the run could not be scheduled: "+err.Error())
		return 0, err
	}
	s.mu.Lock()
	s.seq++
	e.seq = s.seq
	s.queue = append(s.queue, e)
	errs := s.pumpLocked()
	pos := 0
	for i, q := range s.queue {
		if q.d.ID == d.ID {
			pos = i + 1
		}
	}
	s.mu.Unlock()
	s.report(errs)
	return pos, errs[d.ID]
}

func (s *scheduler) report(errs map[string]error) {
	for id, err := range errs {
		log.Printf("dispatch %s: could not start: %v", id, err)
	}
}

// blockedLocked says what keeps e from starting, or "" if nothing does.
func (s *scheduler) blockedLocked(e entry) string {
	if s.paused[e.d.JobID] {
		return "the Job is paused"
	}
	if len(s.running) >= s.limits.Total {
		return fmt.Sprintf("overall cap of %d runs", s.limits.Total)
	}
	siteCap := e.siteCap
	if siteCap <= 0 {
		siteCap = s.limits.PerSite
	}
	site, warden := 0, 0
	for _, o := range s.running {
		if e.site != "" && o.site == e.site {
			site++
		}
		if o.warden == e.warden {
			warden++
		}
	}
	if e.site != "" && site >= siteCap {
		return fmt.Sprintf("Site %s cap of %d runs", e.site, siteCap)
	}
	if warden >= s.limits.PerWarden {
		return fmt.Sprintf("Warden cap of %d Wrights", s.limits.PerWarden)
	}
	return ""
}

// pumpLocked starts every queued dispatch that now fits and returns the ones
// whose launch failed (each already recorded as failed).
func (s *scheduler) pumpLocked() map[string]error {
	errs := map[string]error{}
	if s.r.bg.Err() != nil { // shutting down: queued work stays queued
		return errs
	}
	for i := 0; i < len(s.queue); {
		e := s.queue[i]
		if s.blockedLocked(e) != "" {
			i++
			continue
		}
		s.queue = append(s.queue[:i:i], s.queue[i+1:]...)
		s.running[e.d.ID] = e
		if err := s.startLocked(e); err != nil {
			delete(s.running, e.d.ID)
			errs[e.d.ID] = err
		}
	}
	return errs
}

// setPaused holds back (or releases) a Job's queued dispatches. Runs already
// started carry on.
func (s *scheduler) setPaused(job string, on bool) {
	s.mu.Lock()
	if on {
		s.paused[job] = true
	} else {
		delete(s.paused, job)
	}
	errs := s.pumpLocked()
	s.mu.Unlock()
	s.report(errs)
}

// dequeue removes a waiting dispatch, reporting whether it was waiting.
func (s *scheduler) dequeue(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.queue {
		if e.d.ID == id {
			s.queue = append(s.queue[:i:i], s.queue[i+1:]...)
			return true
		}
	}
	return false
}

func (s *scheduler) wake() {
	s.mu.Lock()
	s.timer = nil
	errs := s.pumpLocked()
	s.mu.Unlock()
	s.report(errs)
}

func (s *scheduler) startLocked(e entry) error {
	err := s.r.st.StartDispatch(e.d.ID)
	if err == nil {
		err = s.r.start(e.d)
	}
	if err != nil {
		s.r.failDispatch(e.d.ID, "the run could not start: "+err.Error())
	}
	return err
}

// finished releases a dispatch's slot once its run is over and starts what now fits.
func (s *scheduler) finished(id string) {
	s.mu.Lock()
	delete(s.running, id)
	errs := s.pumpLocked()
	s.mu.Unlock()
	s.report(errs)
}

func (s *scheduler) status() QueueStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := QueueStatus{Limits: s.limits, Running: []QueueEntry{}, Queued: []QueueEntry{}}
	view := func(e entry) QueueEntry {
		return QueueEntry{DispatchID: e.d.ID, Job: e.d.JobID, Piece: e.d.Piece, Guild: e.d.Guild, Site: e.site}
	}
	var run []entry
	for _, e := range s.running {
		run = append(run, e)
	}
	sort.Slice(run, func(i, j int) bool { return run[i].seq < run[j].seq })
	for _, e := range run {
		st.Running = append(st.Running, view(e))
	}
	for i, e := range s.queue {
		q := view(e)
		q.Position = i + 1
		q.Waiting = s.blockedLocked(e)
		st.Queued = append(st.Queued, q)
	}
	return st
}

func (s *scheduler) stop() {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()
}
