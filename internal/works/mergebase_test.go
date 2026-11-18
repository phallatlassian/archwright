package works

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/forge"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// conflictRig is an open-PR Job whose integration branch and the remote's base
// branch both changed verdict.txt, and whose PR the fake Forge reports as
// conflicting. It returns the integration tip and the base commit.
func conflictRig(t *testing.T, mergeable string) (g *gateRig, f *fakeForge, tip, base string) {
	t.Helper()
	g = newGateRig(t, store.Gates{})
	f = &fakeForge{}
	f.prs.pr = forge.PullRequest{Mergeable: mergeable}
	g.r.UseForge(f)
	if err := g.st.RecordPR(g.job.ID, fakePR); err != nil {
		t.Fatal(err)
	}
	tip = g.commit("verdict.txt", "ours\n")
	site, _ := g.st.GetSite("demo")
	other := t.TempDir()
	git(t, other, "clone", "-q", site.URL, other)
	if err := os.WriteFile(filepath.Join(other, "verdict.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, other, "commit", "-qam", "base moves")
	git(t, other, "push", "-q", "origin", "main")
	base = git(t, other, "rev-parse", "HEAD")
	return g, f, tip, base
}

func TestMergeBaseHasAMergeWrightResolveTheConflictThenPushesAFastForward(t *testing.T) {
	g, f, tip, base := conflictRig(t, "CONFLICTING")
	var out BaseMergeInfo
	g.ok(g.warden, "merge_base", `{}`, &out)
	if out.Status != BaseMergeDispatched || out.Dispatch == nil || out.Dispatch.Piece != "merge-base-1" {
		t.Fatalf("%+v", out)
	}
	if d := g.waitReported("merge-base-1"); d.Status != store.DispatchReported || !strings.Contains(string(d.Result), `"done"`) {
		t.Fatalf("%s: %s", d.Status, d.Result)
	}

	// The merge Wright ran in the integration worktree and committed a merge there.
	reqs := g.runner.Requests()
	if len(reqs) != 1 || reqs[0].Worktree != g.worktree.Path || !strings.Contains(reqs[0].Brief, "base ref: origin/main") || !strings.Contains(reqs[0].Brief, "Never rebase") {
		t.Fatalf("%+v", reqs)
	}
	head := git(t, g.worktree.Path, "rev-parse", "HEAD")
	if parents := git(t, g.worktree.Path, "rev-list", "--parents", "-n1", head); parents != head+" "+tip+" "+base {
		t.Fatalf("not a merge of the old tip and the base: %s", parents)
	}
	if got := git(t, g.worktree.Path, "show", "HEAD:verdict.txt"); got != "ours" {
		t.Fatalf("resolution %q", got)
	}

	// The Warden accepts it and the Works pushes: one push of the integration branch, a fast-forward of the old tip.
	g.ok(g.warden, "accept_piece", `{"piece":"merge-base-1"}`, nil)
	g.ok(g.warden, "push_branch", `{}`, nil)
	if req := pushed(g, f); req.Branch != g.worktree.Branch {
		t.Fatalf("%+v", req)
	}
	git(t, g.repo, "merge-base", "--is-ancestor", tip, g.worktree.Branch)
}

func TestMergeBaseDoesNothingWhenThePRShowsNoConflict(t *testing.T) {
	for _, mergeable := range []string{"MERGEABLE", "UNKNOWN", ""} {
		g, f, tip, _ := conflictRig(t, mergeable)
		var out BaseMergeInfo
		g.ok(g.warden, "merge_base", `{}`, &out)
		if out.Status != BaseMergeNoConflict || out.Mergeable != mergeable || out.Dispatch != nil {
			t.Fatalf("%q: %+v", mergeable, out)
		}
		if len(g.runner.Requests()) != 0 || git(t, g.worktree.Path, "rev-parse", "HEAD") != tip || f.pushCount() != 0 {
			t.Fatalf("%q: something ran", mergeable)
		}
	}
}

func TestMergeBaseNeedsAPRAndTakesNoArgument(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	g.fails(g.warden, "merge_base", `{}`, spool.CodeConflict) // no PR yet
	g, f, _, _ = conflictRig(t, "CONFLICTING")
	for _, in := range []string{`{"base":"other"}`, `{"force":true}`, `{"strategy":"rebase"}`, `[]`} {
		g.fails(g.warden, "merge_base", in, spool.CodeInvalidInput)
	}
	if len(f.prs.reads) != 0 || len(g.runner.Requests()) != 0 {
		t.Fatal("an invalid call reached the Forge or started a Wright")
	}
}

func TestMergeBaseIsForTheWardenOnly(t *testing.T) {
	g, _, _, _ := conflictRig(t, "CONFLICTING")
	for _, role := range []string{RoleWright, RoleArchwright} {
		g.fails(g.wardenFor(role, ""), "merge_base", `{}`, spool.CodeForbidden)
	}
	if len(g.runner.Requests()) != 0 {
		t.Fatal("a Wright was started")
	}
}

func TestMergeBaseRefusesAnIntegrationWorktreeWithUncommittedWork(t *testing.T) {
	g, _, _, _ := conflictRig(t, "CONFLICTING")
	os.WriteFile(filepath.Join(g.worktree.Path, "scratch.txt"), []byte("wip"), 0o644)
	g.fails(g.warden, "merge_base", `{}`, spool.CodeConflict)
	if len(g.runner.Requests()) != 0 {
		t.Fatal("a Wright was started")
	}
}

func TestMergeBaseStartsOnlyOneMergeWrightAtATime(t *testing.T) {
	g, _, _, _ := conflictRig(t, "CONFLICTING")
	g.runner.WithDelay(300 * time.Millisecond)
	var first, second BaseMergeInfo
	g.ok(g.warden, "merge_base", `{}`, &first)
	g.ok(g.warden, "merge_base", `{}`, &second)
	if second.Status != BaseMergeRunning || second.Dispatch == nil || second.Dispatch.Piece != first.Dispatch.Piece {
		t.Fatalf("%+v", second)
	}
	g.waitReported("merge-base-1")
	if n := len(g.runner.Requests()); n != 1 {
		t.Fatalf("%d Wrights", n)
	}
}

func TestPushRefusesABranchRewrittenSinceItWasLastPushed(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	f := &fakeForge{}
	g.r.UseForge(f)
	if err := g.st.RecordPR(g.job.ID, fakePR); err != nil {
		t.Fatal(err)
	}
	pushedTip := g.commit("a.txt", "a")
	git(t, g.repo, "update-ref", "refs/remotes/origin/"+g.worktree.Branch, pushedTip) // as a push leaves it

	// A fast-forward of what was pushed goes out.
	g.commit("b.txt", "b")
	g.ok(g.warden, "push_branch", `{}`, nil)
	if f.pushCount() != 1 {
		t.Fatalf("pushes %d", f.pushCount())
	}

	// Amending the pushed commit rewrites history: nothing is offered to the Forge.
	git(t, g.worktree.Path, "reset", "-q", "--hard", pushedTip+"~1")
	g.commit("c.txt", "c")
	g.fails(g.warden, "push_branch", `{}`, spool.CodeConflict)
	if f.pushCount() != 1 {
		t.Fatalf("a rewritten branch reached the Forge: %d pushes", f.pushCount())
	}
}

// The whole path, with the real gh adapter, a stub gh that reports a conflict,
// a git wrapper that logs every call, a local bare remote and the fake merge
// Wright: the conflict is seen through the PR read, the base is merged into the
// integration branch, and the push reaches the same remote branch as a
// fast-forward. No history-rewriting command is ever issued. Run with
// ARCHWRIGHT_LIVE_BIN to go through the built binary.
func TestMergeBaseEndToEndPushesAFastForwardAndNeverRewrites(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	g.r.UseForge(forge.ByHost{Adapters: map[string]forge.Forge{"git.example.com": forge.GH{}, "github.com": forge.GH{}}})
	dir := t.TempDir()
	src, err := os.ReadFile("../forge/testdata/stub-gh.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitLog := filepath.Join(dir, "git.log")
	wrapper := "#!/bin/sh\necho \"$*\" >> '" + gitLog + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	mergeable := filepath.Join(dir, "mergeable")
	os.WriteFile(mergeable, []byte("MERGEABLE"), 0o644)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_GH_STATE", t.TempDir())
	t.Setenv("STUB_GH_MERGEABLE_FILE", mergeable)
	remote := resolveLocal(t)

	g.planWithOpenPR(true)
	g.commit("a.txt", "a")
	opener := g.wardenFor(RoleWarden, OpenPRStep)
	for _, tool := range []struct{ name, in string }{{"push_branch", `{}`}, {"open_pr", openArgs}} {
		if resp := toolVia(t, opener, tool.name, tool.in); !resp.OK {
			t.Fatalf("%s: %+v", tool.name, resp)
		}
	}
	firstPush := git(t, remote, "rev-parse", g.worktree.Branch)

	// The integration branch gains work while the base moves on the same file.
	ours := g.commit("verdict.txt", "ours\n")
	other := t.TempDir()
	git(t, other, "clone", "-q", "https://git.example.com/o/r.git", other)
	os.WriteFile(filepath.Join(other, "verdict.txt"), []byte("theirs\n"), 0o644)
	git(t, other, "commit", "-qam", "base moves")
	git(t, other, "push", "-q", "origin", "main")
	base := git(t, other, "rev-parse", "HEAD")

	// No conflict yet as far as the Forge says: nothing happens.
	var none BaseMergeInfo
	if resp := toolVia(t, g.warden, "merge_base", `{}`); !resp.OK || json.Unmarshal(resp.Output, &none) != nil || none.Status != BaseMergeNoConflict {
		t.Fatalf("%+v", resp)
	}

	os.WriteFile(mergeable, []byte("CONFLICTING"), 0o644)
	var read PRRead
	if resp := toolVia(t, g.warden, "read_pr", `{}`); !resp.OK || json.Unmarshal(resp.Output, &read) != nil || read.Untrusted.Mergeable != "CONFLICTING" {
		t.Fatalf("%+v", resp)
	}
	var out BaseMergeInfo
	if resp := toolVia(t, g.warden, "merge_base", `{}`); !resp.OK || json.Unmarshal(resp.Output, &out) != nil || out.Status != BaseMergeDispatched {
		t.Fatalf("%+v", resp)
	}
	g.waitReported(out.Dispatch.Piece)
	toolVia(t, g.warden, "accept_piece", `{"piece":"`+out.Dispatch.Piece+`"}`)
	if resp := toolVia(t, g.warden, "push_branch", `{}`); !resp.OK {
		t.Fatalf("%+v", resp)
	}

	head := git(t, g.worktree.Path, "rev-parse", "HEAD")
	if got := git(t, remote, "rev-parse", g.worktree.Branch); got != head {
		t.Fatalf("remote %s, want %s", got, head)
	}
	git(t, remote, "merge-base", "--is-ancestor", firstPush, head) // the same PR's branch moved forward
	git(t, remote, "merge-base", "--is-ancestor", ours, head)
	git(t, remote, "merge-base", "--is-ancestor", base, head)
	if got := git(t, remote, "rev-parse", "main"); got != base {
		t.Fatalf("main moved to %s", got)
	}
	calls, _ := os.ReadFile(gitLog)
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		f := " " + line + " "
		if strings.Contains(f, " push ") && (strings.Contains(f, "force") || strings.Contains(f, " -f ") || strings.Contains(f, " +") || strings.Contains(f, ":+")) {
			t.Fatalf("a forcing push was issued: %s", line)
		}
		for _, bad := range []string{" rebase ", " reset ", " commit --amend", " filter-branch "} {
			if strings.Contains(f, bad) && !strings.Contains(f, "-C "+g.worktree.Path) { // only the test's own setup resets
				t.Fatalf("history-rewriting call by the Works: %s", line)
			}
		}
	}
}

func TestMergeBaseWithTheBaseAlreadyMergedStartsNoWright(t *testing.T) {
	g, _, _, _ := conflictRig(t, "CONFLICTING")
	g.ok(g.warden, "merge_base", `{}`, nil)
	g.waitReported("merge-base-1")
	var out BaseMergeInfo
	g.ok(g.warden, "merge_base", `{}`, &out) // the Forge still says CONFLICTING: the push has not happened yet
	if out.Status != BaseMergeMerged || out.Dispatch != nil || len(g.runner.Requests()) != 1 {
		t.Fatalf("%+v, %d Wrights", out, len(g.runner.Requests()))
	}
}
