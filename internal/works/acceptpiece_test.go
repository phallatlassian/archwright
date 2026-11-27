package works

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

func accept(g *gateRig, piece string) store.Piece {
	g.t.Helper()
	var out store.Piece
	g.ok(g.warden, "accept_piece", `{"piece":"`+piece+`"}`, &out)
	return out
}

// wardenMerge plays the Warden merging a piece branch into the integration
// branch in its own worktree (ADR 0030). A conflict is aborted, as the Warden
// does, and its files are returned.
func (g *gateRig) wardenMerge(branch string) (conflicts []string) {
	g.t.Helper()
	cmd := exec.Command("git", "-C", g.worktree.Path, "-c", "user.name=Warden", "-c", "user.email=warden@archwright.invalid", "-c", "commit.gpgsign=false", "merge", "--no-edit", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		files := git(g.t, g.worktree.Path, "diff", "--name-only", "--diff-filter=U")
		if files == "" {
			g.t.Fatalf("merge failed without a conflict: %v: %s", err, out)
		}
		git(g.t, g.worktree.Path, "merge", "--abort")
		return strings.Fields(files)
	}
	return nil
}

func TestAcceptPieceOnlyRecordsAcceptanceAndTheWardensMergeFeedsTheGates(t *testing.T) {
	g := newGateRig(t, store.Gates{Test: "test -f work.txt"}) // the gate passes only on a tree holding the piece's work
	g.runner.WriteFiles(map[string]string{"work.txt": "w"})
	g.dispatch("p")
	d := g.waitReported("p")
	before := git(t, g.worktree.Path, "rev-parse", "HEAD")

	if got := accept(g, "p"); got.Status != store.PieceAccepted {
		t.Fatalf("%+v", got)
	}
	if after := git(t, g.worktree.Path, "rev-parse", "HEAD"); after != before {
		t.Fatalf("accept_piece ran a merge in the integration worktree: %s -> %s", before, after)
	}
	if reqs := g.runner.Requests(); len(reqs) != 1 {
		t.Errorf("accepting ran an agent: %d runs", len(reqs))
	}

	if c := g.wardenMerge(d.Branch); len(c) != 0 {
		t.Fatalf("a clean piece conflicted: %v", c)
	}
	if w := git(t, g.worktree.Path, "show", "HEAD:work.txt"); w != "w" {
		t.Errorf("work.txt = %q", w)
	}
	g.ok(g.warden, "handback", handbackJSON("done", `"integration"`, ""), nil)
	if run := g.shownGates(); run.Status != store.GateRunPassed {
		t.Fatalf("gates on the integration branch: %+v", run)
	}
}

// A conflicting piece goes back to its own Wright, which merges the integration
// branch into its piece branch; the Warden's second merge is then clean. No
// merge Wright is ever started.
func TestAConflictingPieceIsSentBackToItsWrightAndMergesCleanlyTheSecondTime(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	g.runner.WriteFiles(map[string]string{"verdict.txt": "theirs\n"})
	g.dispatch("p")
	round0 := g.waitReported("p")
	g.commit("verdict.txt", "ours\n")
	tip := git(t, g.worktree.Path, "rev-parse", "HEAD")

	conflicts := g.wardenMerge(round0.Branch)
	if len(conflicts) != 1 || conflicts[0] != "verdict.txt" {
		t.Fatalf("conflicts: %v", conflicts)
	}
	if now := git(t, g.worktree.Path, "rev-parse", "HEAD"); now != tip || git(t, g.worktree.Path, "status", "--porcelain") != "" {
		t.Fatal("the aborted merge left the integration worktree changed")
	}
	if p, _ := g.st.GetPiece(g.job.ID, "p"); p.Status != store.PieceOpen {
		t.Fatalf("a conflicting piece was accepted: %+v", p)
	}

	reason := "verdict.txt conflicts with the integration branch\n- base ref: " + g.worktree.Branch + "\n"
	g.ok(g.warden, "rework_wright", `{"piece":"p","reason":`+quote(reason)+`,"diagnosis":"d","brief":`+goodBrief+`}`, nil)
	round1 := g.waitReported("p")
	if round1.Round != 1 || !strings.Contains(string(round1.Result), `"done"`) {
		t.Fatalf("%+v", round1)
	}

	if c := g.wardenMerge(round1.Branch); len(c) != 0 {
		t.Fatalf("the second merge conflicted: %v", c)
	}
	if got := git(t, g.worktree.Path, "show", "HEAD:verdict.txt"); got != "theirs" {
		t.Errorf("verdict.txt = %q", got)
	}
	if got := accept(g, "p"); got.Status != store.PieceAccepted {
		t.Fatalf("%+v", got)
	}
	pieces, err := g.st.Pieces(g.job.ID)
	if err != nil || len(pieces) != 1 {
		t.Fatalf("a piece other than p exists: %+v %v", pieces, err)
	}
	for _, r := range g.runner.Requests() {
		if r.Piece != "p" {
			t.Errorf("an agent other than p's Wright ran: %+v", r)
		}
	}
}

func TestReworkRoundStartsFromThePreviousRoundsCommittedWork(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	g.runner.script = map[string][]WrightResult{"p": {{Status: ResultBlocked, Uncertainty: "cannot run the suite"}, doneResult}}
	g.runner.WriteFiles(map[string]string{"fix.txt": "round 0 work\n"})
	g.dispatch("p")
	round0 := g.waitReported("p")

	var d DispatchInfo
	g.ok(g.warden, "rework_wright", `{"piece":"p","reason":"missed tests","diagnosis":"d","brief":`+goodBrief+`}`, &d)
	round1 := g.waitReported("p")
	if round1.Round != 1 || round0.Branch == "" || round1.Branch == "" || round1.Branch == round0.Branch {
		t.Fatalf("each round has its own branch: %q %q", round0.Branch, round1.Branch)
	}
	want := git(t, g.repo, "rev-parse", round0.Branch)
	if base := git(t, g.repo, "merge-base", round1.Branch, round0.Branch); base != want {
		t.Errorf("round 1 did not branch from round 0's committed work: base %s, want %s", base, want)
	}
	if got := git(t, g.repo, "show", round1.Branch+":fix.txt"); got != "round 0 work" {
		t.Errorf("round 1 does not hold round 0's file: %q", got)
	}
	reqs := g.runner.Requests()
	if brief := reqs[len(reqs)-1].Brief; !strings.Contains(brief, "continues from the previous round's work") || !strings.Contains(brief, round0.Branch) {
		t.Errorf("the rework brief does not say what it continues from:\n%s", brief)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
