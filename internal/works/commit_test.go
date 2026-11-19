package works

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// A sandboxed Codex Wright cannot write its worktree's git directory, so the
// Works commits for it (ADR 0022). These tests play the Wright with a stub
// runner that edits files in its worktree and calls the commit tool through
// the spool, as a real Wright would; no agent is started.

type wrightTool func(tool, input string) spool.Response

// playWright dispatches piece p and runs act as its Wright. It returns every
// tool response, in call order.
func (g *gateRig) playWright(piece string, act func(req RunRequest, call wrightTool)) []spool.Response {
	g.t.Helper()
	var mu sync.Mutex
	var resps []spool.Response
	g.runner.WithAct(func(ctx context.Context, req RunRequest) error {
		call := func(tool, input string) spool.Response {
			mu.Lock()
			id := "wr" + string(rune('a'+len(resps)))
			mu.Unlock()
			resp, err := spool.Call(ctx, spool.Env{Run: req.Env.Run, Dir: req.Env.Spool, Token: req.Env.Token}, id, tool, json.RawMessage(input), 5*time.Second)
			if err != nil {
				resp = spool.Response{ID: id, Error: &spool.Error{Code: "call_failed", Message: err.Error()}}
			}
			mu.Lock()
			resps = append(resps, resp)
			mu.Unlock()
			return resp
		}
		act(req, call)
		return nil
	})
	g.dispatch(piece)
	mu.Lock()
	defer mu.Unlock()
	return append([]spool.Response(nil), resps...)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantCommitted(t *testing.T, r spool.Response) Committed {
	t.Helper()
	if !r.OK {
		t.Fatalf("commit: %+v", r.Error)
	}
	var out Committed
	if err := json.Unmarshal(r.Output, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWrightCommitStagesAndCommitsInItsOwnWorktree(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	tip := git(t, g.worktree.Path, "rev-parse", "HEAD")
	var wt string
	resps := g.playWright("login", func(req RunRequest, call wrightTool) {
		wt = req.Worktree
		writeFile(t, filepath.Join(wt, "login.go"), "package login\n")
		writeFile(t, filepath.Join(wt, "auth/token.go"), "package auth\n")
		call("commit", `{"message":"feat: add login\n\nbody line"}`)
	})
	if len(resps) != 1 {
		t.Fatalf("%+v", resps)
	}
	out := wantCommitted(t, resps[0])
	if wt == "" || wt == g.worktree.Path {
		t.Fatalf("the Wright worked in %q, not its own worktree", wt)
	}
	head := git(t, wt, "rev-parse", "HEAD")
	branch := git(t, wt, "symbolic-ref", "--short", "HEAD")
	if out.Status != "committed" || out.SHA != head || out.Branch != branch || strings.Join(out.Files, ",") != "auth/token.go,login.go" {
		t.Fatalf("%+v head %s on %s", out, head, branch)
	}
	if subj := git(t, wt, "log", "-1", "--format=%B"); subj != "feat: add login\n\nbody line" {
		t.Fatalf("message %q", subj)
	}
	if status := git(t, wt, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree not clean: %s", status)
	}
	if git(t, wt, "rev-parse", "HEAD~1") != tip {
		t.Fatal("the commit is not on top of the integration tip")
	}
	// the integration worktree and the base branch are untouched
	if git(t, g.worktree.Path, "rev-parse", "HEAD") != tip || git(t, g.repo, "rev-parse", "refs/remotes/origin/main") == head {
		t.Fatal("another branch moved")
	}
}

func TestWrightCommitStagesOnlyTheNamedPaths(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	var wt string
	resps := g.playWright("p", func(req RunRequest, call wrightTool) {
		wt = req.Worktree
		writeFile(t, filepath.Join(wt, "a.txt"), "a")
		writeFile(t, filepath.Join(wt, "b.txt"), "b")
		writeFile(t, filepath.Join(wt, "dir/c.txt"), "c")
		call("commit", `{"message":"only a and dir","paths":["a.txt","dir"]}`)
	})
	if out := wantCommitted(t, resps[0]); strings.Join(out.Files, ",") != "a.txt,dir/c.txt" {
		t.Fatalf("%+v", out)
	}
	if status := git(t, wt, "status", "--porcelain"); status != "?? b.txt" {
		t.Fatalf("status %q", status)
	}
}

func TestWrightCommitWithNothingStagedMakesNoCommit(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	var wt string
	resps := g.playWright("p", func(req RunRequest, call wrightTool) {
		wt = req.Worktree
		call("commit", `{"message":"nothing"}`)
	})
	out := wantCommitted(t, resps[0])
	if out.Status != "nothing_to_commit" || out.SHA != git(t, wt, "rev-parse", "HEAD") || len(out.Files) != 0 {
		t.Fatalf("%+v", out)
	}
}

func TestWrightCommitNeverStagesTheSkillsTheWorksDelivered(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g.r.UseDefinitions(reg)
	g.r.UseSkills(SkillsDelivery{Catalogue: skills.Vendored(), Runner: "claude", RunDir: func(id string) string { return filepath.Join(t.TempDir(), id) }})
	var wt string
	resps := g.playWright("p", func(req RunRequest, call wrightTool) {
		wt = req.Worktree
		if _, err := os.Stat(filepath.Join(wt, ".agents/skills")); err != nil {
			t.Errorf("no skills were delivered: %v", err)
		}
		writeFile(t, filepath.Join(wt, "real.txt"), "work")
		call("commit", `{"message":"work"}`)
	})
	if out := wantCommitted(t, resps[0]); strings.Join(out.Files, ",") != "real.txt" {
		t.Fatalf("staged %v", out.Files)
	}
	if tracked := git(t, wt, "ls-files"); strings.Contains(tracked, ".agents") || strings.Contains(tracked, ".claude") {
		t.Fatalf("skills were committed:\n%s", tracked)
	}
}

func TestWrightCommitTakesNoTargetArgument(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	tip := git(t, g.worktree.Path, "rev-parse", "HEAD")
	other := t.TempDir()
	resps := g.playWright("p", func(req RunRequest, call wrightTool) {
		writeFile(t, filepath.Join(req.Worktree, "x.txt"), "x")
		writeFile(t, filepath.Join(g.worktree.Path, "y.txt"), "y")
		for _, in := range []string{
			`{"message":"m","worktree":"` + g.worktree.Path + `"}`,
			`{"message":"m","dir":"` + other + `"}`,
			`{"message":"m","branch":"main"}`,
			`{"message":"m","job_id":"` + g.job.ID + `"}`,
			`{"message":"m","amend":true}`,
			`{"message":"m","no_verify":false}`,
			`{"message":"m","paths":["/etc/passwd"]}`,
			`{"message":"m","paths":["../escape.txt"]}`,
			`{"message":"m","paths":["a/../../escape.txt"]}`,
			`{"message":"m","paths":[""]}`,
			`{"message":""}`,
			`{}`,
			`[]`,
		} {
			call("commit", in)
		}
	})
	for i, r := range resps {
		if r.OK || r.Error.Code != spool.CodeInvalidInput {
			t.Errorf("call %d: %+v", i, r)
		}
	}
	if len(resps) != 13 {
		t.Fatalf("%d calls answered", len(resps))
	}
	if git(t, g.worktree.Path, "rev-parse", "HEAD") != tip || git(t, g.worktree.Path, "status", "--porcelain") != "?? y.txt" {
		t.Fatal("the integration worktree was touched")
	}
	if _, err := os.Stat(filepath.Join(other, ".git")); err == nil {
		t.Fatal("another directory was touched")
	}
}

func TestCommitIsForWrightsOnly(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	tip := git(t, g.worktree.Path, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(g.worktree.Path, "dirty.txt"), "x")
	for _, env := range []RunEnv{g.warden, g.arch, g.wardenFor(RoleWarden, OpenPRStep)} {
		g.fails(env, "commit", `{"message":"m"}`, spool.CodeForbidden)
	}
	// a Wright run that carries out no dispatch has no worktree of its own
	loose, err := g.r.StartFor(RoleWright, t.TempDir(), RunContext{Job: g.job.ID, Guild: "masonry"})
	if err != nil {
		t.Fatal(err)
	}
	g.fails(loose, "commit", `{"message":"m"}`, spool.CodeInvalidInput)
	if git(t, g.worktree.Path, "rev-parse", "HEAD") != tip {
		t.Fatal("a commit was made")
	}
}

func TestWrightCommitIsRefusedOnceTheWrightLeftItsBranch(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	resps := g.playWright("p", func(req RunRequest, call wrightTool) {
		writeFile(t, filepath.Join(req.Worktree, "x.txt"), "x")
		git(t, req.Worktree, "checkout", "-q", "-b", "elsewhere")
		call("commit", `{"message":"m"}`)
	})
	if resps[0].OK || resps[0].Error.Code != spool.CodeConflict {
		t.Fatalf("%+v", resps[0])
	}
}

func TestWrightCommitIsRefusedForAPausedJob(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	resps := g.playWright("p", func(req RunRequest, call wrightTool) {
		writeFile(t, filepath.Join(req.Worktree, "x.txt"), "x")
		if err := g.st.PauseJob(g.job.ID); err != nil {
			t.Error(err)
		}
		call("commit", `{"message":"m"}`)
	})
	if resps[0].OK || resps[0].Error.Code != spool.CodeConflict {
		t.Fatalf("%+v", resps[0])
	}
}

func TestWrightCommitRunsNoHooksAndNeedsNoSigning(t *testing.T) {
	g := newGateRig(t, store.Gates{})
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hooks := filepath.Join(g.repo, "hooks")
	writeFile(t, filepath.Join(hooks, "pre-commit"), "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
	os.Chmod(filepath.Join(hooks, "pre-commit"), 0o755)
	writeFile(t, filepath.Join(hooks, "commit-msg"), "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
	os.Chmod(filepath.Join(hooks, "commit-msg"), 0o755)
	git(t, g.repo, "config", "core.hooksPath", hooks)
	git(t, g.repo, "config", "commit.gpgsign", "true")
	git(t, g.repo, "config", "gpg.program", "false") // a signing attempt would fail
	resps := g.playWright("p", func(req RunRequest, call wrightTool) {
		writeFile(t, filepath.Join(req.Worktree, "x.txt"), "x")
		call("commit", `{"message":"m"}`)
	})
	wantCommitted(t, resps[0])
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository hook ran")
	}
}

func TestMergeWrightCommitsTheMergeInTheIntegrationWorktree(t *testing.T) {
	g, _, tip, base := conflictRig(t, "CONFLICTING")
	var got spool.Response
	g.runner.WithAct(func(ctx context.Context, req RunRequest) error {
		git(t, req.Worktree, "merge", "--no-commit", "-X", "ours", "origin/main")
		resp, err := spool.Call(ctx, spool.Env{Run: req.Env.Run, Dir: req.Env.Spool, Token: req.Env.Token}, "merge-commit", "commit", json.RawMessage(`{"message":"Merge origin/main"}`), 5*time.Second)
		if err != nil {
			return err
		}
		got = resp
		return nil
	})
	g.ok(g.warden, "merge_base", `{}`, nil)
	g.waitReported("merge-base-1")
	out := wantCommitted(t, got)
	head := git(t, g.worktree.Path, "rev-parse", "HEAD")
	if out.SHA != head || out.Branch != g.worktree.Branch {
		t.Fatalf("%+v head %s", out, head)
	}
	if parents := git(t, g.worktree.Path, "rev-list", "--parents", "-n1", head); parents != head+" "+tip+" "+base {
		t.Fatalf("not a merge of the old tip and the base: %s", parents)
	}
}
