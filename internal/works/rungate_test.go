package works

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// wrightRig is a gate rig with one dispatched piece and a Wright run bound to
// it, whose worktree is the one the Works made for the dispatch.
type wrightRig struct {
	*gateRig
	wright RunEnv
	wt     string
}

func newWrightRig(t *testing.T, gates store.Gates) *wrightRig {
	g := newGateRig(t, gates)
	g.runner.script = map[string][]WrightResult{"p": {doneResult}}
	g.dispatch("p")
	disp, err := g.st.LatestDispatch(g.job.ID, "p")
	if err != nil {
		t.Fatal(err)
	}
	// the dispatch's own worktree is released once it reports, so give the
	// Wright run a live one the way a running dispatch has it
	wtr, err := g.sm.CreateWorktree(g.job.ID, store.KindWright, "")
	if err != nil {
		t.Fatal(err)
	}
	wt := wtr.Path
	g.r.mu.Lock()
	g.r.wrightWts[disp.ID] = wt
	g.r.mu.Unlock()
	env, err := g.r.StartFor(RoleWright, t.TempDir(), RunContext{Job: g.job.ID, Guild: "masonry", DispatchID: disp.ID})
	if err != nil {
		t.Fatal(err)
	}
	return &wrightRig{gateRig: g, wright: env, wt: wt}
}

func (w *wrightRig) write(name, content string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.wt, name), []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wrightRig) gate(input string) GateRan {
	w.t.Helper()
	var out GateRan
	w.ok(w.wright, "run_gate", input, &out)
	return out
}

func TestRunGateRunsTheGatesOnASnapshotOfTheWrightsUncommittedWork(t *testing.T) {
	w := newWrightRig(t, store.Gates{Test: "grep -q good verdict.txt && test -f new.txt"})
	if got := w.gate(`{}`); got.Status != store.GateRunFailed {
		t.Fatalf("the unchanged worktree lacks new.txt, want failed: %+v", got)
	}
	w.write("new.txt", "untracked")
	got := w.gate(`{"gate":"test"}`)
	if got.Status != store.GateRunPassed || len(got.Results) != 1 || got.Results[0].Gate != store.GateTest {
		t.Fatalf("an untracked file is part of the snapshot: %+v", got)
	}
	w.write("verdict.txt", "bad\n")
	if got := w.gate(`{}`); got.Status != store.GateRunFailed {
		t.Fatalf("a tracked edit is part of the snapshot: %+v", got)
	}
}

func TestRunGateNeverWritesIntoTheWrightsWorktree(t *testing.T) {
	w := newWrightRig(t, store.Gates{Test: "touch scribble.txt && git add -A"})
	w.gate(`{}`)
	if _, err := os.Stat(filepath.Join(w.wt, "scribble.txt")); err == nil {
		t.Error("a gate wrote into the Wright's worktree")
	}
	if out := git(t, w.wt, "status", "--porcelain"); out != "" {
		t.Errorf("the Wright's worktree changed: %q", out)
	}
}

func TestRunGateLeavesNoCheckoutBehindAndSkipsScaffolding(t *testing.T) {
	w := newWrightRig(t, store.Gates{Test: "! test -e .claude/settings.json"})
	if err := os.MkdirAll(filepath.Join(w.wt, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.write(".claude/settings.json", "{}")
	if got := w.gate(`{}`); got.Status != store.GateRunPassed {
		t.Fatalf("scaffolding was copied into the snapshot: %+v", got)
	}
	wts, _ := w.st.Worktrees(w.job.ID)
	list := git(t, w.repo, "worktree", "list", "--porcelain")
	if n := strings.Count(list, "worktree "); n != len(wts)+1 { // + the clone itself
		t.Errorf("worktrees after the call: %d registered in git, %d recorded:\n%s", n, len(wts), list)
	}
}

func TestRunGateIsForWrightsAndTakesOnlyAGateName(t *testing.T) {
	w := newWrightRig(t, store.Gates{Test: "true"})
	w.fails(w.warden, "run_gate", `{}`, spool.CodeForbidden)
	w.fails(w.arch, "run_gate", `{}`, spool.CodeForbidden)
	w.fails(w.wright, "run_gate", `{"gate":"make check"}`, spool.CodeInvalidInput)
	w.fails(w.wright, "run_gate", `{"command":"go test ./..."}`, spool.CodeInvalidInput)
}

func TestRunGateIsCappedPerDispatch(t *testing.T) {
	w := newWrightRig(t, store.Gates{Test: "true"})
	var last GateRan
	for i := 0; i < DefaultRunGateCap; i++ {
		last = w.gate(`{}`)
	}
	if last.CallsLeft != 0 {
		t.Fatalf("calls left after the cap: %d", last.CallsLeft)
	}
	w.fails(w.wright, "run_gate", `{}`, spool.CodeConflict)
}

func TestRunGateRunsOneAtATimePerJob(t *testing.T) {
	w := newWrightRig(t, store.Gates{Test: "true"})
	_, release, err := w.r.takeGateSlot(w.job.ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	w.fails(w.wright, "run_gate", `{}`, spool.CodeConflict)
	release()
	w.gate(`{}`)
}
