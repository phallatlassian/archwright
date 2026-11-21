package works

import (
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

func accept(g *gateRig, piece string) AcceptInfo {
	g.t.Helper()
	var out AcceptInfo
	g.ok(g.warden, "accept_piece", `{"piece":"`+piece+`"}`, &out)
	return out
}

func TestAcceptPieceLandsTheBranchOnTheIntegrationBranchWithoutAnAgentRun(t *testing.T) {
	g := newGateRig(t, store.Gates{Test: "test -f work.txt"}) // the gate passes only on a tree holding the piece's work
	g.runner.WriteFiles(map[string]string{"work.txt": "w", ".claude/settings.json": "{}"})
	g.dispatch("p")
	before := git(t, g.worktree.Path, "rev-parse", "HEAD")

	got := accept(g, "p")
	if got.Status != store.PieceAccepted || got.Merge != "merged" {
		t.Fatalf("%+v", got)
	}
	if reqs := g.runner.Requests(); len(reqs) != 1 {
		t.Errorf("a clean merge ran an agent: %d runs", len(reqs))
	}
	if after := git(t, g.worktree.Path, "rev-parse", "HEAD"); after == before {
		t.Error("the integration branch did not move")
	}
	if w := git(t, g.worktree.Path, "show", "HEAD:work.txt"); w != "w" {
		t.Errorf("work.txt = %q", w)
	}
	if tip := git(t, g.repo, "rev-parse", g.worktree.Branch); tip != git(t, g.worktree.Path, "rev-parse", "HEAD") {
		t.Error("the Works' clone and the integration worktree disagree")
	}

	// the handback gates run on that tip, and so do the push and PR head (the branch's tip)
	g.ok(g.warden, "handback", handbackJSON("done", `"integration"`, ""), nil)
	if run := g.shownGates(); run.Status != store.GateRunPassed {
		t.Fatalf("gates on the integration branch: %+v", run)
	}
}

func TestAcceptPieceWithNoCommitsIsRecordedAndChangesNothing(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	g.runner.WriteFiles(nil)
	g.dispatch("p")
	before := git(t, g.worktree.Path, "rev-parse", "HEAD")
	got := accept(g, "p")
	if got.Status != store.PieceAccepted || got.Merge != "unchanged" {
		t.Fatalf("%+v", got)
	}
	if after := git(t, g.worktree.Path, "rev-parse", "HEAD"); after != before {
		t.Errorf("the integration branch moved: %s -> %s", before, after)
	}
}

func TestAcceptPieceOnAConflictLeavesThePieceOpenAndDispatchesAMergeWright(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	g.runner.WriteFiles(map[string]string{"verdict.txt": "theirs\n"})
	g.dispatch("p")
	tip := g.commit("verdict.txt", "ours\n")

	got := accept(g, "p")
	if got.Status != AcceptConflict || got.Merge != "conflict" || len(got.Conflicts) != 1 || got.Conflicts[0] != "verdict.txt" ||
		got.Dispatch == nil || got.Dispatch.Piece != "merge-piece-1" {
		t.Fatalf("%+v", got)
	}
	if p, _ := g.st.GetPiece(g.job.ID, "p"); p.Status != store.PieceOpen {
		t.Errorf("a conflicting piece was accepted: %+v", p)
	}
	if d := g.waitReported("merge-piece-1"); !strings.Contains(string(d.Result), `"done"`) {
		t.Fatalf("%s: %s", d.Status, d.Result)
	}
	reqs := g.runner.Requests()
	if last := reqs[len(reqs)-1]; last.Worktree != g.worktree.Path || !strings.Contains(last.Brief, "conflicted files: verdict.txt") {
		t.Fatalf("the merge Wright did not work in the integration worktree: %+v", last)
	}
	head := git(t, g.worktree.Path, "rev-parse", "HEAD")
	if parents := git(t, g.worktree.Path, "rev-list", "--parents", "-n1", head); !strings.HasPrefix(parents, head+" "+tip+" ") {
		t.Fatalf("the merge Wright did not merge onto the old tip: %s", parents)
	}

	// accepting the merge piece merges nothing more; accepting the piece again finds it already in
	if m := accept(g, "merge-piece-1"); m.Status != store.PieceAccepted || m.Merge != "" {
		t.Fatalf("%+v", m)
	}
	if again := accept(g, "p"); again.Status != store.PieceAccepted || again.Merge != "unchanged" {
		t.Fatalf("%+v", again)
	}
	if after := git(t, g.worktree.Path, "rev-parse", "HEAD"); after != head {
		t.Error("accepting after the merge Wright moved the branch")
	}
}
