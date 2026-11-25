package works

import (
	"cmp"
	"errors"
	"fmt"
	"log"
	"os"

	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

// What a restarted Works makes of the Sessions a stopped one left (ADR 0029):
// a live pane is adopted by its recorded id, a lost pane's Session is resumed by
// its session id when its piece or step next runs, and what cannot be resumed
// is run afresh from the stored brief, as it was before Sessions stayed open.

// RestoredSession is a Session a stopped Works recorded: the newest run of one
// piece's Wright or one Job's Warden of a Guild, and where it lived.
type RestoredSession struct {
	Key       string   // where the Runner keeps it: wrightKey or wardenKey
	Job       string   // the Job that holds it
	Run       string   // the newest run recorded against it
	Runs      []string // every run recorded against its pane: the pane must be one of theirs
	Pane      string
	SessionID string // the vendor's session id, "" if its SessionStart never arrived
	Dir       string // where it works
	Runner    string // the vendor CLI it runs
}

// Restoration is what became of a RestoredSession.
type Restoration string

const (
	// Adopted: its pane is alive and the Runner follows it again.
	Adopted Restoration = "adopted"
	// Resumable: its pane is gone but the vendor can resume it by id, which the
	// next run of its piece or step does.
	Resumable Restoration = "resumable"
	// Lost: its pane is gone and it cannot be resumed; the next run of its
	// piece or step is a fresh Session running the stored brief.
	Lost Restoration = "lost"
)

// RestoreResult is one session's outcome.
type RestoreResult struct {
	RestoredSession
	Outcome Restoration
	State   hosted.State // what an adopted Session is doing now
	Err     error        // why an adoption failed, when it was not simply that the pane was gone
}

// resumable is a Session of an earlier Works to resume by id.
type resumable struct{ id, dir, runner string }

// SessionRestorer is a set of Sessions that can take over those a stopped
// Works left (SessionRunner).
type SessionRestorer interface {
	// Restore takes over what it can of the Sessions, newest per key first, and
	// returns what became of each, and the live Session panes none of them
	// accounts for, which are flagged and never killed.
	Restore(sessions []RestoredSession) ([]RestoreResult, []hosted.Orphan)
}

// Restore implements SessionRestorer.
func (a *SessionRunner) Restore(sessions []RestoredSession) ([]RestoreResult, []hosted.Orphan) {
	known := map[string]bool{}
	var results []RestoreResult
	for _, rs := range sessions {
		res := RestoreResult{RestoredSession: rs, Outcome: Lost}
		if s, err := a.adopt(rs); err == nil {
			a.mu.Lock()
			if a.live == nil {
				a.live = map[string]*liveSession{}
			}
			ls := &liveSession{job: rs.Job, s: s, r: a.Runners[rs.Runner], key: rs.Key, worktree: cmp.Or(s.Info().Dir, rs.Dir), turns: s.Info().Turns}
			// Its events record against the run it was last carrying out, until a run takes it over.
			ls.cur = &carry{req: RunRequest{Env: RunEnv{Run: rs.Run}}, finished: true}
			a.live[rs.Key] = ls
			a.mu.Unlock()
			go a.follow(ls)
			known[rs.Pane] = true
			res.Outcome, res.State = Adopted, s.State()
		} else {
			if !errors.Is(err, hosted.ErrNoPane) {
				// The pane is there but could not be taken over: it is left running and
				// is neither an orphan nor resumed beside itself.
				res.Err = err
				known[rs.Pane] = true
				log.Printf("session %s of run %s: adopting it: %v; it is left running, unmanaged", rs.Pane, rs.Run, err)
				results = append(results, res)
				continue
			}
			if rn := a.Runners[rs.Runner]; rn != nil && rn.Capabilities().Resume && rs.SessionID != "" {
				if _, err := os.Stat(rs.Dir); err == nil {
					a.mu.Lock()
					if a.resumable == nil {
						a.resumable = map[string]*resumable{}
					}
					a.resumable[rs.Key] = &resumable{id: rs.SessionID, dir: rs.Dir, runner: rs.Runner}
					a.mu.Unlock()
					res.Outcome = Resumable
				}
			}
		}
		results = append(results, res)
	}
	var orphans []hosted.Orphan
	flagged := map[hosted.Runner]bool{}
	for _, rn := range a.Runners {
		o, ok := rn.(hosted.Orphaner)
		if !ok || flagged[rn] {
			continue
		}
		flagged[rn] = true
		found, err := o.FlagOrphans(known)
		if err != nil {
			log.Printf("looking for orphaned Session panes: %v", err)
		}
		orphans = append(orphans, found...)
	}
	return results, orphans
}

// adopt takes over the live pane of a recorded Session, by the Runner that
// launched it.
func (a *SessionRunner) adopt(rs RestoredSession) (hosted.Session, error) {
	rn := a.Runners[rs.Runner]
	if rn == nil {
		return nil, fmt.Errorf("%w: it ran %q, which this Works has no Runner for", hosted.ErrNoPane, rs.Runner)
	}
	ad, ok := rn.(hosted.Adopter)
	if !ok {
		return nil, fmt.Errorf("%w: the %s Runner cannot adopt Sessions", hosted.ErrNoPane, rs.Runner)
	}
	return ad.Adopt(rs.Pane, rs.Runs...)
}

// takeResumable is the Session of an earlier Works that a run of key may
// resume instead of starting afresh, and forgets it once taken: a Session is
// resumed once. The Runner must be the one that ran it, able to resume, and
// spec must allow the run to continue it.
func (a *SessionRunner) takeResumable(key string, r hosted.Runner, spec runSpec) *resumable {
	a.mu.Lock()
	rs := a.resumable[key]
	a.mu.Unlock()
	if rs == nil || rs.runner != r.Name() || !r.Capabilities().Resume || !spec.reuse(&liveSession{worktree: rs.dir}) {
		return nil
	}
	if _, err := os.Stat(rs.dir); err != nil {
		return nil
	}
	a.mu.Lock()
	delete(a.resumable, key)
	a.mu.Unlock()
	return rs
}

// SessionRecovery says what RecoverSessions found.
type SessionRecovery struct {
	Adopted, Resumable, Lost int
	Orphans                  []hosted.Orphan
}

// RecoverSessions takes over the Sessions a stopped Works left in their panes
// (ADR 0029), by the pane ids recorded on its runs. A live pane is adopted. One
// whose pane is gone is resumed by its session id when its piece or step next
// runs, and otherwise run afresh from the stored brief, which is what the
// dispatches InterruptDispatches queued are. Live panes no recorded run accounts
// for are flagged as orphans and never killed. It runs once, at start, before
// the queue resumes. A Works that hosts no Sessions finds nothing.
func (r *Runs) RecoverSessions() (SessionRecovery, error) {
	var out SessionRecovery
	r.mu.Lock()
	js := r.sessions
	r.mu.Unlock()
	rest, ok := js.(SessionRestorer)
	if !ok {
		return out, nil
	}
	recs, err := r.st.LiveSessionRuns()
	if err != nil {
		return out, err
	}
	var items []RestoredSession
	seen := map[string]bool{}
	for _, rec := range recs { // newest first: the newest record of a key is the Session it holds now
		key, ok := r.sessionKey(rec)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		runs, err := r.st.PaneRuns(rec.PaneID)
		if err != nil {
			return out, err
		}
		items = append(items, RestoredSession{Key: key, Job: rec.JobID, Run: rec.RunID, Runs: runs, Pane: rec.PaneID, SessionID: rec.SessionID, Dir: rec.SessionDir, Runner: rec.Runner})
	}
	results, orphans := rest.Restore(items)
	for _, res := range results {
		switch res.Outcome {
		case Adopted:
			out.Adopted++
			r.recordSession(res.Run, store.SessionRecord{State: string(res.State)})
			r.recoveryEvent(res.Job, "session.adopted", map[string]any{"run": res.Run, "pane": res.Pane})
		case Resumable:
			out.Resumable++
			r.recoveryEvent(res.Job, "session.resumable", map[string]any{"run": res.Run, "session_id": res.SessionID})
		default:
			out.Lost++
			r.recoveryEvent(res.Job, "session.lost", map[string]any{"run": res.Run, "pane": res.Pane})
		}
		if res.Outcome == Lost {
			r.recordSession(res.Run, store.SessionRecord{State: string(hosted.StateEnded)}) // not looked for again
		}
	}
	for _, o := range orphans {
		log.Printf("session pane %s (run %s, %s) belongs to no run this Works recorded: flagged as an orphan and left running", o.Pane, o.Run, o.Role)
		r.recoveryEvent("", "session.orphan", map[string]any{"pane": o.Pane, "run": o.Run, "role": o.Role})
	}
	out.Orphans = orphans
	return out, nil
}

// sessionKey is where the SessionRunner keeps the Session of a recorded run.
func (r *Runs) sessionKey(rec store.RunRecord) (string, bool) {
	switch {
	case rec.DispatchID != "":
		d, err := r.st.GetDispatch(rec.DispatchID)
		if err != nil {
			return "", false
		}
		return wrightKey(rec.JobID, d.Piece), true
	case rec.Guild != "":
		return wardenKey(rec.JobID, rec.Guild), true
	}
	return "", false
}

func (r *Runs) recordSession(run string, rec store.SessionRecord) {
	if err := r.st.SetRunSession(run, rec); err != nil {
		log.Printf("run %s: recording its Session: %v", run, err)
	}
}

func (r *Runs) recoveryEvent(job, kind string, payload map[string]any) {
	if err := r.st.AppendEvent(job, kind, payload); err != nil {
		log.Printf("recording %s: %v", kind, err)
	}
}
