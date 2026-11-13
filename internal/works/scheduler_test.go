package works

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

// holdRunner parks each run until the test releases its piece, then reports
// through a FakeRunner. fail, if set, may refuse a run with an error first.
type holdRunner struct {
	inner *FakeRunner
	mu    sync.Mutex
	hold  map[string]chan struct{}
	fail  func(piece string, attempt int) error
	tries map[string]int
}

func newHoldRunner() *holdRunner {
	return &holdRunner{inner: NewFakeRunner(nil), hold: map[string]chan struct{}{}, tries: map[string]int{}}
}

func (h *holdRunner) gate(piece string) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hold[piece] == nil {
		h.hold[piece] = make(chan struct{})
	}
	return h.hold[piece]
}

func (h *holdRunner) release(piece string) { close(h.gate(piece)) }

func (h *holdRunner) Run(ctx context.Context, req RunRequest) error {
	h.mu.Lock()
	h.tries[req.Piece]++
	n := h.tries[req.Piece]
	h.mu.Unlock()
	if h.fail != nil {
		if err := h.fail(req.Piece, n); err != nil {
			return err
		}
	}
	select {
	case <-h.gate(req.Piece):
	case <-ctx.Done():
		return ctx.Err()
	}
	return h.inner.Run(ctx, req)
}

func (h *holdRunner) attempts(piece string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tries[piece]
}

func newHeldRig(t *testing.T, limits Limits) (*rig, *holdRunner) {
	g := newRig(t, nil)
	h := newHoldRunner()
	g.r.Configure(func(name string) (Guild, error) { return Guild{Name: name}, nil }, h)
	g.r.SetLimits(limits)
	return g, h
}

func (g *rig) send(piece string) DispatchInfo {
	var d DispatchInfo
	g.ok(g.warden, "dispatch_wright", `{"piece":"`+piece+`","brief":`+goodBrief+`}`, &d)
	return d
}

func (g *rig) queue() QueueStatus {
	var q QueueStatus
	g.ok(g.arch, "show_queue", `{}`, &q)
	return q
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func pieces(es []QueueEntry) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Piece)
	}
	return out
}

func TestWardenCapQueuesTheExcessFIFOAndVisibly(t *testing.T) {
	g, h := newHeldRig(t, Limits{PerWarden: 2})
	if d := g.send("a"); d.Status != store.DispatchRunning {
		t.Fatalf("a: %+v", d)
	}
	g.send("b")
	c, d := g.send("c"), g.send("d")
	if c.Status != store.DispatchQueued || c.QueuePos != 1 || d.Status != store.DispatchQueued || d.QueuePos != 2 {
		t.Fatalf("over-cap dispatches must queue in order: %+v %+v", c, d)
	}
	q := g.queue()
	if fmt.Sprint(pieces(q.Running)) != "[a b]" || fmt.Sprint(pieces(q.Queued)) != "[c d]" || q.Queued[0].Waiting == "" || q.Limits.PerWarden != 2 {
		t.Fatalf("queue not visible: %+v", q)
	}
	if got, _ := g.st.LatestDispatch(g.job.ID, "c"); got.Status != store.DispatchQueued {
		t.Fatalf("a queued dispatch is stored as queued, got %s", got.Status)
	}
	g.fails(g.warden, "handback", `{}`, "conflict") // queued pieces still count as running work

	h.release("a")
	eventually(t, "c to start", func() bool { return fmt.Sprint(pieces(g.queue().Running)) == "[b c]" })
	if fmt.Sprint(pieces(g.queue().Queued)) != "[d]" {
		t.Fatalf("d must still wait: %+v", g.queue())
	}
	h.release("b")
	eventually(t, "d to start", func() bool { return fmt.Sprint(pieces(g.queue().Running)) == "[c d]" })
	h.release("c")
	h.release("d")
	g.waitReported("d")
}

func TestOverallAndSiteCapsAcrossJobs(t *testing.T) {
	g, h := newHeldRig(t, Limits{Total: 3, PerSite: 2, PerWarden: 3})
	for _, n := range []string{"alpha", "beta"} {
		if _, err := g.st.AddSite(store.Site{Name: n, URL: "https://h/o/" + n, ClonePath: "/c/" + n, BaseBranch: "main", ConcurrencyCap: 2}); err != nil {
			t.Fatal(err)
		}
	}
	wardenFor := func(site string) RunEnv {
		j, err := g.st.CreateJobOn(site, "J "+site, "")
		if err != nil {
			t.Fatal(err)
		}
		w, err := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: j.ID, Guild: "engineering"})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	wa, wb := wardenFor("alpha"), wardenFor("beta")
	send := func(w RunEnv, piece string) DispatchInfo {
		var d DispatchInfo
		g.ok(w, "dispatch_wright", `{"piece":"`+piece+`","brief":`+goodBrief+`}`, &d)
		return d
	}
	if send(wa, "a1").Status != "running" || send(wa, "a2").Status != "running" {
		t.Fatal("two runs fit the Site cap")
	}
	if a3 := send(wa, "a3"); a3.Status != "queued" {
		t.Fatalf("third run on alpha exceeds the Site cap: %+v", a3)
	}
	if b1 := send(wb, "b1"); b1.Status != "running" {
		t.Fatalf("another Site is unaffected by alpha's cap: %+v", b1)
	}
	// Overall cap is now full (a1 a2 b1); b2 queues behind a3 though its Site has room.
	if b2 := send(wb, "b2"); b2.Status != "queued" || b2.QueuePos != 2 {
		t.Fatalf("overall cap: %+v", b2)
	}
	h.release("b1") // frees an overall slot; a3 is still blocked by its Site, so b2 jumps ahead of it
	eventually(t, "b2 to start", func() bool { return fmt.Sprint(pieces(g.queue().Running)) == "[a1 a2 b2]" })
	if fmt.Sprint(pieces(g.queue().Queued)) != "[a3]" {
		t.Fatalf("a3 should wait for its Site: %+v", g.queue())
	}
	h.release("a1")
	eventually(t, "a3 to start", func() bool { return fmt.Sprint(pieces(g.queue().Running)) == "[a2 a3 b2]" })
	for _, p := range []string{"a2", "a3", "b2"} {
		h.release(p)
	}
}

func TestSitesOwnCapReplacesTheDefault(t *testing.T) {
	g, _ := newHeldRig(t, Limits{})
	if _, err := g.st.AddSite(store.Site{Name: "solo", URL: "https://h/o/solo", ClonePath: "/c/solo", BaseBranch: "main", ConcurrencyCap: 1}); err != nil {
		t.Fatal(err)
	}
	j, _ := g.st.CreateJobOn("solo", "J", "")
	w, _ := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: j.ID, Guild: "engineering"})
	var a, b DispatchInfo
	g.ok(w, "dispatch_wright", `{"piece":"a","brief":`+goodBrief+`}`, &a)
	g.ok(w, "dispatch_wright", `{"piece":"b","brief":`+goodBrief+`}`, &b)
	if a.Status != "running" || b.Status != "queued" {
		t.Fatalf("%+v %+v", a, b)
	}
	if g.queue().Limits != DefaultLimits {
		t.Fatalf("defaults: %+v", g.queue().Limits)
	}
}

func TestRateLimitedRunnerPausesWithBackoffAndTheDispatchRetries(t *testing.T) {
	g, h := newHeldRig(t, Limits{})
	g.r.sched.backoffBase, g.r.sched.backoffMax = 250*time.Millisecond, 500*time.Millisecond
	h.fail = func(piece string, n int) error {
		if piece == "a" && n <= 2 {
			return &RateLimitError{Err: fmt.Errorf("429")}
		}
		return nil
	}
	h.release("a")
	h.release("b")
	g.send("a")
	eventually(t, "the runner to be paused", func() bool { return g.queue().Paused != nil })
	q := g.queue()
	if len(q.Running) != 0 || fmt.Sprint(pieces(q.Queued)) != "[a]" || q.Paused.Failures != 1 {
		t.Fatalf("a rate-limited dispatch is requeued, not failed: %+v", q)
	}
	if d, _ := g.st.LatestDispatch(g.job.ID, "a"); d.Status == store.DispatchRejected || d.Result != nil {
		t.Fatalf("dispatch must not fail: %+v", d)
	}
	// While paused nothing new starts, but it is accepted and queued behind a.
	if b := g.send("b"); b.Status != store.DispatchQueued || b.QueuePos != 2 {
		t.Fatalf("paused runner queues new work: %+v", b)
	}
	d := g.waitReported("a")
	if d.Status != store.DispatchReported || h.attempts("a") != 3 {
		t.Fatalf("a should succeed on its third attempt: %s after %d", d.Status, h.attempts("a"))
	}
	g.waitReported("b")
	if g.queue().Paused != nil {
		t.Fatalf("pause clears once the runner answers: %+v", g.queue())
	}
}

func TestBackoffGrowsAndHonoursRetryAfter(t *testing.T) {
	g, h := newHeldRig(t, Limits{})
	s := g.r.sched
	s.backoffBase, s.backoffMax = time.Hour, 3*time.Hour
	now := time.Now()
	s.now = func() time.Time { return now }
	h.fail = func(string, int) error { return &RateLimitError{} }
	g.send("a")
	eventually(t, "first pause", func() bool { return g.queue().Paused != nil })
	if p := g.queue().Paused; p.Until.Sub(now) != time.Hour {
		t.Fatalf("first backoff = base: %v", p.Until.Sub(now))
	}
	s.mu.Lock()
	e := s.queue[0]
	s.running[e.d.ID], s.queue = e, nil // simulate the retry having started
	s.mu.Unlock()
	now = now.Add(2 * time.Hour) // the first pause is over
	g.st.StartDispatch(e.d.ID)
	s.rateLimited(e.d.ID, &RateLimitError{RetryAfter: 10 * time.Hour})
	if p := g.queue().Paused; p.Failures != 2 || p.Until.Sub(now) != 10*time.Hour {
		t.Fatalf("a longer vendor hint wins: %+v", p)
	}
}

func scopedBrief(scope string, readOnly bool) string {
	b := map[string]any{"objective": "x", "scope": map[string]any{"in": []string{scope}}, "constraints": []string{},
		"inputs": []string{"i"}, "definition_of_done": []string{"d"}, "return_shape": "r", "limits": map[string]int{"timeout_minutes": 5}, "read_only": readOnly}
	raw, _ := json.Marshal(b)
	return string(raw)
}

func TestOverlappingScopesWarnInNeedsYouWithoutBlocking(t *testing.T) {
	g, h := newHeldRig(t, Limits{})
	if _, err := g.st.AddSite(store.Site{Name: "shop", URL: "https://h/o/shop", ClonePath: "/c/shop", BaseBranch: "main", ConcurrencyCap: 3}); err != nil {
		t.Fatal(err)
	}
	mk := func() (store.Job, RunEnv) {
		j, _ := g.st.CreateJobOn("shop", "J", "")
		w, _ := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: j.ID, Guild: "engineering"})
		return j, w
	}
	j1, w1 := mk()
	j2, w2 := mk()
	j3, w3 := mk()
	send := func(w RunEnv, piece, scope string, ro bool) DispatchInfo {
		var d DispatchInfo
		g.ok(w, "dispatch_wright", `{"piece":"`+piece+`","brief":`+scopedBrief(scope, ro)+`}`, &d)
		return d
	}
	inbox := func() []store.NeedsYouItem {
		var items []store.NeedsYouItem
		g.ok(g.arch, "show_needs_you", `{}`, &items)
		return items
	}
	send(w1, "p1", "auth/", false)
	if len(inbox()) != 0 {
		t.Fatalf("no overlap yet: %+v", inbox())
	}
	if d := send(w2, "p2", "auth/login/**", false); d.Status != store.DispatchRunning {
		t.Fatalf("an overlapping Job must still run: %+v", d)
	}
	items := inbox()
	if len(items) != 1 || items[0].Kind != store.NeedsScopeOverlap || items[0].JobID != j2.ID {
		t.Fatalf("want one scope-overlap warning: %+v", items)
	}
	var detail struct{ Jobs []string }
	json.Unmarshal(items[0].Detail, &detail)
	if fmt.Sprint(detail.Jobs) != fmt.Sprint([]string{j2.ID, j1.ID}) {
		t.Fatalf("warning names both Jobs: %s", items[0].Detail)
	}
	send(w2, "p2b", "auth/login", false) // same overlap again: no duplicate warning
	send(w3, "p3", "billing/", false)    // disjoint
	send(w3, "p3r", "auth/", true)       // read-only writes nothing
	if got := inbox(); len(got) != 2 {   // p2b overlaps p1 on a distinct scope pair, p3/p3r add none
		t.Fatalf("unexpected warnings: %+v", got)
	}
	_ = j3
	for _, p := range []string{"p1", "p2", "p2b", "p3", "p3r"} {
		h.release(p)
	}
}

func TestScopesOverlap(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"auth/", "auth/login.go", true}, {"./auth", "auth/**", true}, {"auth", "authz", false},
		{"a/b", "a/c", false}, {"*", "anything", true}, {"/", "x/y", true}, {"x/y.go", "x/y.go", true},
	} {
		if got := scopesOverlap(c.a, c.b); got != c.want {
			t.Errorf("%q ~ %q = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestResumeRequeuesDispatchesLeftQueued(t *testing.T) {
	g, h := newHeldRig(t, Limits{PerWarden: 1})
	g.send("a")
	g.send("b")
	g.r.sched.mu.Lock()
	g.r.sched.queue = nil // as if the Works had restarted: b is queued in the database only
	g.r.sched.mu.Unlock()
	if err := g.r.Resume(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(pieces(g.queue().Queued)) != "[b]" {
		t.Fatalf("%+v", g.queue())
	}
	h.release("a")
	h.release("b")
	g.waitReported("b")
}

func TestLimitsFromEnv(t *testing.T) {
	env := map[string]string{"ARCHWRIGHT_MAX_RUNS": "6", "ARCHWRIGHT_MAX_WRIGHTS_PER_WARDEN": "junk"}
	if got := LimitsFromEnv(func(k string) string { return env[k] }); got != (Limits{Total: 6, PerSite: 3, PerWarden: 3}) {
		t.Fatalf("%+v", got)
	}
}

func TestScopesOverlapGlobs(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"src/*.go", "src/foo.go", true},
		{"internal/**/store.go", "internal", true},
		{"internal/**/store.go", "internal/x/y.go", true},
		{"src/fo*.go", "src/foo.go", true},
		{"docs/**", "src/a.go", false},
		{"src/a?.go", "lib/a.go", false},
	}
	for _, c := range cases {
		if got := scopesOverlap(c.a, c.b); got != c.want {
			t.Errorf("scopesOverlap(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}
