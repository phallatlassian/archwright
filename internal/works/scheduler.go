package works

import (
	"errors"
	"fmt"
	"log"
	"sort"
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

// RateLimitError is what a Runner returns when its vendor refuses the run for
// rate or usage limits. The Works pauses the Runner with backoff and retries
// the dispatch rather than failing it.
type RateLimitError struct {
	RetryAfter time.Duration // the vendor's hint, if it gave one
	Err        error
}

func (e *RateLimitError) Error() string {
	if e.Err != nil {
		return "rate limited: " + e.Err.Error()
	}
	return "rate limited"
}

func (e *RateLimitError) Unwrap() error { return e.Err }

const (
	defaultBackoffBase = 2 * time.Second
	defaultBackoffMax  = 5 * time.Minute
)

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

// Pause says the Runner is paused after rate-limit errors.
type Pause struct {
	Until    time.Time `json:"until"`
	Failures int       `json:"failures"` // consecutive rate-limited runs, which set the backoff
}

// QueueStatus is the visible state of the scheduler.
type QueueStatus struct {
	Limits  Limits       `json:"limits"`
	Running []QueueEntry `json:"running"`
	Queued  []QueueEntry `json:"queued"` // FIFO
	Paused  *Pause       `json:"paused,omitempty"`
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

	mu          sync.Mutex
	limits      Limits
	backoffBase time.Duration
	backoffMax  time.Duration
	now         func() time.Time
	seq         int
	running     map[string]entry
	queue       []entry
	pausedUntil time.Time
	failures    int
	timer       *time.Timer
}

func newScheduler(r *Runs) *scheduler {
	return &scheduler{r: r, limits: DefaultLimits, backoffBase: defaultBackoffBase, backoffMax: defaultBackoffMax, now: time.Now, running: map[string]entry{}}
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
func (r *Runs) Queue() QueueStatus { return r.sched.status() }

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
	if wait := s.pausedUntil.Sub(s.now()); wait > 0 {
		if s.timer != nil {
			s.timer.Stop()
		}
		s.timer = time.AfterFunc(wait, s.wake)
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
	s.failures = 0 // the Runner is answering again
	errs := s.pumpLocked()
	s.mu.Unlock()
	s.report(errs)
}

// rateLimited puts a dispatch back at its place in the queue and pauses the
// Runner: for the vendor's hint or an exponential backoff, whichever is longer.
func (s *scheduler) rateLimited(id string, rl *RateLimitError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.running[id]
	delete(s.running, id)
	if !ok {
		return
	}
	if err := s.r.st.RequeueDispatch(id); err != nil {
		log.Printf("dispatch %s: requeue after rate limit: %v", id, err)
		return
	}
	s.failures++
	wait := s.backoffBase
	for i := 1; i < s.failures && wait < s.backoffMax; i++ {
		wait *= 2
	}
	wait = min(wait, s.backoffMax)
	wait = max(wait, rl.RetryAfter)
	if until := s.now().Add(wait); until.After(s.pausedUntil) {
		s.pausedUntil = until
	}
	at := sort.Search(len(s.queue), func(i int) bool { return s.queue[i].seq > e.seq })
	s.queue = append(s.queue, entry{})
	copy(s.queue[at+1:], s.queue[at:])
	s.queue[at] = e
	s.report(s.pumpLocked())
}

func (s *scheduler) status() QueueStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := QueueStatus{Limits: s.limits, Running: []QueueEntry{}, Queued: []QueueEntry{}}
	if s.pausedUntil.After(s.now()) {
		st.Paused = &Pause{Until: s.pausedUntil, Failures: s.failures}
	}
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
		if st.Paused != nil {
			q.Waiting = "the runner is paused until " + st.Paused.Until.UTC().Format(time.RFC3339)
		}
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

func asRateLimit(err error) (*RateLimitError, bool) {
	var rl *RateLimitError
	return rl, errors.As(err, &rl)
}
