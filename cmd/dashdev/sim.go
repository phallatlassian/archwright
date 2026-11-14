package main

import (
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
	"github.com/phallatlassian/archwright/internal/works"
)

// The simulation plays a few Jobs through their Plans so the board can be watched changing: each Job
// works toward a target (some steps done, one running or blocked, or the whole Job accepted), pausing a
// random while between moves. Once it gets there it holds for a moment, then starts again from the
// beginning. Every move goes out on the event stream, as the real Works' would.

// simSpec is where a simulated Job is heading.
type simSpec struct {
	targets map[string]string // step id -> done, running or blocked; a step not listed stays pending
	holder  string            // who holds the ask a blocked step raises
	accept  bool              // the Job ends accepted once every step is done
}

var simSpecs = map[string]simSpec{
	"j-7f3a21": {targets: map[string]string{"explore": "done", "implement": "running"}},
	"j-91be55": {targets: map[string]string{"trace-code": "done", "trace-docs": "done", "plan-fix": "blocked"}, holder: store.HolderArchwright},
	"j-a17c30": {targets: map[string]string{"trace-code": "done", "trace-docs": "done", "plan-fix": "done", "fix": "running"}},
	"j-c4d9e1": {targets: map[string]string{"trace-code": "done", "trace-docs": "done", "plan-fix": "done", "fix": "done", "review": "done"}, accept: true},
	"j-e05b62": {targets: map[string]string{"audit": "done", "bump-runtime": "running", "bump-dev": "blocked"}, holder: store.HolderWarden},
}

type move struct {
	kind string // start, finish, block or accept
	step store.Step
}

// planMoves lists the moves that take a Plan from nothing to its target: a layer's steps all start, then
// the ones that finish do, and a running or blocked step ends the run since nothing after it can start.
func planMoves(steps []store.Step, sp simSpec) []move {
	depth := map[string]int{}
	var of func(s store.Step) int
	by := map[string]store.Step{}
	for _, s := range steps {
		by[s.ID] = s
	}
	of = func(s store.Step) int {
		if d, ok := depth[s.ID]; ok {
			return d
		}
		d := 0
		for _, b := range s.BlockedBy {
			if bs, ok := by[b]; ok && of(bs)+1 > d {
				d = of(bs) + 1
			}
		}
		depth[s.ID] = d
		return d
	}
	var layers [][]store.Step
	for _, s := range steps {
		d := of(s)
		for len(layers) <= d {
			layers = append(layers, nil)
		}
		layers[d] = append(layers[d], s)
	}
	var out []move
	allDone := true
	for _, l := range layers {
		var active []store.Step
		for _, s := range l {
			if sp.targets[s.ID] != "" {
				active = append(active, s)
			}
		}
		for _, s := range active {
			out = append(out, move{"start", s})
		}
		stop := false
		for _, s := range active {
			switch sp.targets[s.ID] {
			case "done":
				out = append(out, move{"finish", s})
			case "blocked":
				out = append(out, move{"block", s})
				stop = true
			default:
				stop = true
			}
		}
		if stop || len(active) < len(l) {
			allDone = false
			break
		}
	}
	if allDone && sp.accept {
		out = append(out, move{kind: "accept"})
	}
	return out
}

// simulate starts one goroutine per simulated Job and one that keeps running runs consuming tokens.
func (s *server) simulate() {
	ids := make([]string, 0, len(simSpecs))
	for id := range simSpecs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s.world.mu.Lock()
		j := s.world.job(id)
		s.world.mu.Unlock()
		if j != nil && j.Plan != nil {
			go s.playJob(j, simSpecs[id])
		}
	}
	go func() {
		for range time.Tick(1500 * time.Millisecond) {
			s.world.mu.Lock()
			s.world.burn()
			s.world.mu.Unlock()
			s.announce()
		}
	}()
}

func pause(minS, maxS float64) {
	time.Sleep(time.Duration((minS + rand.Float64()*(maxS-minS)) * float64(time.Second)))
}

func (s *server) playJob(j *mockJob, sp simSpec) {
	w := s.world
	moves := planMoves(j.Plan.Steps, sp)
	w.mu.Lock()
	w.reset(j)
	next := rand.Intn(len(moves) + 1) // join the story partway through, so the board starts varied
	for _, m := range moves[:next] {
		w.apply(j, m, sp)
	}
	w.mu.Unlock()
	s.announce()
	for {
		if next >= len(moves) {
			pause(10, 16) // hold at the target, then go again from the start
			w.mu.Lock()
			w.reset(j)
			next = 0
			w.mu.Unlock()
			s.announce()
		}
		pause(2, 6)
		if next >= len(moves) {
			continue
		}
		w.mu.Lock()
		w.apply(j, moves[next], sp)
		w.mu.Unlock()
		next++
		s.announce()
	}
}

// reset puts a Job back to before its first step: no dispatches, runs, asks or reviews, and not closed.
func (w *mockWorld) reset(j *mockJob) {
	for _, r := range j.Runs {
		delete(w.Events, r.RunID)
	}
	j.Dispatches, j.Runs, j.Asks, j.Reviews = nil, nil, nil, nil
	j.Job.Outcome = ""
	keepQ := func(es []works.QueueEntry) []works.QueueEntry {
		out := []works.QueueEntry{}
		for _, e := range es {
			if e.Job != j.Job.ID {
				out = append(out, e)
			}
		}
		return out
	}
	w.Queue.Running, w.Queue.Queued = keepQ(w.Queue.Running), keepQ(w.Queue.Queued)
	w.NeedsYou = filterItems(w.NeedsYou, func(it store.NeedsYouItem) bool { return !(it.Kind == store.NeedsAsk && it.JobID == j.Job.ID) })
}

func (w *mockWorld) next() int { w.seq++; return w.seq }

func (j *mockJob) dispatchFor(piece string) *store.Dispatch {
	for i := len(j.Dispatches) - 1; i >= 0; i-- {
		if j.Dispatches[i].Piece == piece {
			return &j.Dispatches[i]
		}
	}
	return nil
}

func (j *mockJob) runFor(dispatchID string) *store.RunRecord {
	for i := range j.Runs {
		if j.Runs[i].DispatchID == dispatchID {
			return &j.Runs[i]
		}
	}
	return nil
}

func (w *mockWorld) dequeue(dispatchID string) {
	out := []works.QueueEntry{}
	for _, e := range w.Queue.Running {
		if e.DispatchID != dispatchID {
			out = append(out, e)
		}
	}
	w.Queue.Running = out
}

// apply makes one move: the Works dispatching a Wright, a run reporting, a run ending on an ask, or the
// Job being accepted.
func (w *mockWorld) apply(j *mockJob, m move, sp simSpec) {
	now := time.Now().UTC()
	switch m.kind {
	case "start":
		n := w.next()
		did, rid := fmt.Sprintf("d-sim-%d", n), fmt.Sprintf("r-sim-%d", n)
		runner := []string{"claude", "codex"}[rand.Intn(2)]
		j.Dispatches = append(j.Dispatches, dispatch(did, j.Job.ID, m.step.ID, m.step.Guild, 0, store.DispatchRunning, rid, now))
		j.Runs = append(j.Runs, run(rid, j.Job.ID, did, m.step.Guild, runner, 0, 0, 0, "running", now, 0))
		w.Events[rid] = runEvents(rid, now, 1, false)
		w.Queue.Running = append(w.Queue.Running, works.QueueEntry{DispatchID: did, Job: j.Job.ID, Piece: m.step.ID, Guild: m.step.Guild, Site: j.Job.Site})
	case "finish", "block":
		d := j.dispatchFor(m.step.ID)
		if d == nil {
			return
		}
		if r := j.runFor(d.ID); r != nil {
			r.Status, r.EndedAt = "done", tptr(now)
		}
		w.dequeue(d.ID)
		if m.kind == "finish" {
			d.Status = store.DispatchReported
			return
		}
		d.Status = store.DispatchBlocked
		a := store.Ask{ID: fmt.Sprintf("ask-sim-%d", w.next()), JobID: j.Job.ID, Asker: "wright", Guild: m.step.Guild, Piece: m.step.ID, DispatchID: d.ID,
			Kind: store.AskDecision, Blocking: true, Why: "The Wright reached a choice it should not make alone.", Holder: sp.holder, Status: store.AskOpen,
			Answers: []store.Answer{}, CreatedAt: now,
			Questions: []store.Question{{ID: "q1", Text: "How should " + m.step.ID + " go ahead?", Options: []string{"Take the safe option", "Take the faster option"}, Default: "Take the safe option"}}}
		j.Asks = append(j.Asks, a)
		if sp.holder == store.HolderArchwright {
			w.NeedsYou = append(w.NeedsYou, store.NeedsYouItem{ID: int64(w.next()) + 1000, Kind: store.NeedsAsk, JobID: j.Job.ID, Site: j.Job.Site,
				Summary: "Ask " + a.ID + " reached the Archwright.", Detail: []byte(`{}`), CreatedAt: now})
		}
	case "accept":
		j.Reviews = []store.Review{{ID: fmt.Sprintf("rv-sim-%d", w.next()), JobID: j.Job.ID, Seq: 1, Branch: "aw/" + j.Job.ID, SHA: "5e6f7a8", Status: store.ReviewReported,
			SpecBasis: store.SpecPlanAndRequest, Overall: store.VerdictPass, CreatedAt: now,
			Standards: &store.Axis{Verdict: store.VerdictPass, Findings: []store.Finding{}}, Spec: &store.Axis{Verdict: store.VerdictPass, Findings: []store.Finding{}}}}
		j.Job.Outcome = "accepted"
	}
	// A new run's first event, and a step that has just ended, show in the run timeline.
	if m.kind != "accept" {
		if d := j.dispatchFor(m.step.ID); d != nil && d.RunID != "" && m.kind != "start" {
			w.addEvent(d.RunID, "usage", "run ended", now)
		}
	}
}

func (w *mockWorld) addEvent(runID, kind, summary string, at time.Time) {
	evs := w.Events[runID]
	w.Events[runID] = append(evs, store.RunEvent{RunID: runID, Seq: len(evs) + 1, Kind: kind, Summary: summary, HasRaw: true, At: at})
}

// burn makes every running run consume a little more, as a live Wright would.
func (w *mockWorld) burn() {
	now := time.Now().UTC()
	for _, j := range w.Jobs {
		for i := range j.Runs {
			r := &j.Runs[i]
			if r.Status != "running" || rand.Intn(2) == 0 {
				continue
			}
			r.InputTokens += 600 + rand.Intn(2400)
			r.OutputTokens += 80 + rand.Intn(420)
			if r.Runner == "claude" {
				r.CostUSD += float64(r.InputTokens%900+300) / 100000
			}
			if len(w.Events[r.RunID]) < 24 {
				w.addEvent(r.RunID, []string{"message", "tool_call", "tool_result"}[rand.Intn(3)], []string{"Reading the code", "Editing a file", "Running the tests"}[rand.Intn(3)], now)
			}
		}
	}
}
