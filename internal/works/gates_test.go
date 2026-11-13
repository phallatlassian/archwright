package works

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/store"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gateRig is a rig whose Job belongs to a registered Site with an
// integration branch the Wardens' handbacks are about.
type gateRig struct {
	*rig
	sm       *sites.Manager
	repo     string
	worktree store.Worktree
}

func newGateRig(t *testing.T, gates store.Gates) *gateRig {
	g := newRig(t, nil)
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	repo = filepath.Join(repo, "repo")
	os.Mkdir(repo, 0o755)
	git(t, repo, "init", "-b", "main")
	os.WriteFile(filepath.Join(repo, "verdict.txt"), []byte("good\n"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "init")

	root, _ := filepath.EvalSymlinks(t.TempDir())
	sm := sites.New(g.st, filepath.Join(root, "worktrees"))
	if _, err := sm.Register(sites.RegisterInput{Name: "demo", Path: repo, BaseBranch: "main", Gates: &gates}); err != nil {
		t.Fatal(err)
	}
	g.r.UseSites(sm)
	job, err := g.st.CreateJobOn("demo", "J", "")
	if err != nil {
		t.Fatal(err)
	}
	g.job = job
	if g.warden, err = g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: job.ID, Guild: "engineering"}); err != nil {
		t.Fatal(err)
	}
	wt, err := sm.CreateWorktree(job.ID, store.KindIntegration, "")
	if err != nil {
		t.Fatal(err)
	}
	return &gateRig{rig: g, sm: sm, repo: repo, worktree: wt}
}

// commit changes a file on the integration branch.
func (g *gateRig) commit(name, content string) string {
	os.MkdirAll(filepath.Dir(filepath.Join(g.worktree.Path, name)), 0o755)
	os.WriteFile(filepath.Join(g.worktree.Path, name), []byte(content), 0o644)
	git(g.t, g.worktree.Path, "add", ".")
	git(g.t, g.worktree.Path, "commit", "-m", "change "+name)
	return git(g.t, g.worktree.Path, "rev-parse", "HEAD")
}

// handBack accepts a piece and hands back, then waits for the Works' gate run.
func (g *gateRig) handBack(piece string) store.GateRun {
	g.t.Helper()
	g.dispatch(piece)
	g.ok(g.warden, "accept_piece", `{"piece":"`+piece+`"}`, nil)
	g.ok(g.warden, "handback", handbackJSON("done", `"integration"`, ""), nil)
	return g.shownGates()
}

func (g *gateRig) shownGates() store.GateRun {
	g.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var shown struct{ Gates store.GateRun }
		g.ok(g.arch, "show_handback", `{"job_id":"`+g.job.ID+`"}`, &shown)
		if shown.Gates.Status != store.GateRunRunning {
			return shown.Gates
		}
	}
	g.t.Fatal("gates never finished")
	return store.GateRun{}
}

func TestGatesAreRerunOnTheHandbackBranchAndRecorded(t *testing.T) {
	g := newGateRig(t, store.Gates{Typecheck: "true", Test: "grep -q good verdict.txt"})
	sha := g.commit("notes.md", "hello")
	run := g.handBack("p")
	if run.Status != store.GateRunPassed || run.SHA != sha || run.Branch != g.worktree.Branch || run.FinishedAt == nil {
		t.Fatalf("%+v", run)
	}
	if len(run.Results) != 3 || run.Results[0].Status != store.GatePassed || run.Results[1].Status != store.GateSkipped || run.Results[2].Status != store.GatePassed {
		t.Fatalf("%+v", run.Results)
	}
	if run.Results[2].Command != "grep -q good verdict.txt" {
		t.Fatalf("%+v", run.Results[2])
	}
	// the temporary checkout is gone
	if out := git(t, g.repo, "worktree", "list"); strings.Contains(out, "-gates") {
		t.Fatalf("gate checkout left behind:\n%s", out)
	}
}

func TestFailingGateIsRecordedWhateverTheWardenClaims(t *testing.T) {
	g := newGateRig(t, store.Gates{Test: "grep -q good verdict.txt"})
	g.commit("verdict.txt", "broken\n")
	g.dispatch("p")
	g.ok(g.warden, "accept_piece", `{"piece":"p"}`, nil)
	// the Warden vouches for the work; the Works re-runs the gate anyway
	g.ok(g.warden, "handback", `{"status":"done","results":["integration"],"checked":["all gates pass, trust me"],"rejected":[],"uncertainty":"none"}`, nil)
	run := g.shownGates()
	if run.Status != store.GateRunFailed || run.Results[2].Status != store.GateFailed || run.Results[2].ExitCode != 1 {
		t.Fatalf("%+v", run)
	}
}

func TestBranchCannotWeakenTheGates(t *testing.T) {
	g := newGateRig(t, store.Gates{Typecheck: "echo registry-typecheck; exit 1", Lint: "echo registry-lint", Test: "echo registry-test"})
	// the branch rewrites every kind of repo-side check config it can
	g.commit(".archwright.json", `{"gates":{"typecheck":"true","test":"true"}}`)
	g.commit("package.json", `{"scripts":{"typecheck":"true","lint":"true","test":"true"}}`)
	g.commit("Makefile", "test:\n\ttrue\n")
	g.commit(".golangci.yml", "linters:\n  disable-all: true\n")
	run := g.handBack("p")
	if run.Status != store.GateRunFailed {
		t.Fatalf("the branch's config changed the outcome: %+v", run)
	}
	for i, want := range []string{"registry-typecheck", "registry-lint", "registry-test"} {
		if !strings.Contains(run.Results[i].Command, want) || !strings.Contains(run.Results[i].Output, want) {
			t.Errorf("%s ran %q -> %q", run.Results[i].Gate, run.Results[i].Command, run.Results[i].Output)
		}
	}
}

func TestGateCommandChangeNeedsApprovalAndAppliesToLaterHandbacks(t *testing.T) {
	g := newGateRig(t, store.Gates{Test: "echo old"})
	c, err := g.st.ProposeGates("demo", store.Gates{Test: "echo new"})
	if err != nil {
		t.Fatal(err)
	}
	if run := g.handBack("p"); run.Results[2].Command != "echo old" {
		t.Fatalf("a pending proposal changed what runs: %+v", run.Results[2])
	}
	if _, err := g.st.ApproveGateChange(c.ID); err != nil {
		t.Fatal(err)
	}
	g.ok(g.warden, "handback", handbackJSON("done", `"again"`, ""), nil)
	if run := g.shownGates(); run.Results[2].Command != "echo new" {
		t.Fatalf("%+v", run.Results[2])
	}
}

func TestHandbackWithNothingToRunGatesOnRecordsNotRun(t *testing.T) {
	g := newRig(t, nil) // a Job with no Site
	g.dispatch("p")
	g.ok(g.warden, "accept_piece", `{"piece":"p"}`, nil)
	g.ok(g.warden, "handback", handbackJSON("done", `"x"`, ""), nil)
	var shown struct{ Gates store.GateRun }
	g.ok(g.arch, "show_handback", `{"job_id":"`+g.job.ID+`"}`, &shown)
	if shown.Gates.Status != store.GateRunNotRun || shown.Gates.Reason == "" {
		t.Fatalf("%+v", shown.Gates)
	}

	noGates := newGateRig(t, store.Gates{})
	if run := noGates.handBack("p"); run.Status != store.GateRunNotRun {
		t.Fatalf("%+v", run)
	}
}

func TestSlowGateIsKilledAtTheTimeout(t *testing.T) {
	g := newGateRig(t, store.Gates{Test: "sleep 30"})
	g.r.SetGateTimeout(200 * time.Millisecond)
	run := g.handBack("p")
	if run.Status != store.GateRunFailed || !strings.Contains(run.Results[2].Output, "timed out") {
		t.Fatalf("%+v", run)
	}
}

func TestGateChangesOverAPIRequireApproval(t *testing.T) {
	d := dirs(t)
	var n atomic.Int32
	c, err := Connect(context.Background(), d, inProcess(t, &n))
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, repo, "init", "-b", "main")
	git(t, repo, "commit", "--allow-empty", "-m", "init")
	in := sites.RegisterInput{Name: "r", Path: repo, BaseBranch: "main", Gates: &store.Gates{Typecheck: "tc", Lint: "lint", Test: "t"}}
	if err := c.Do("POST", "/sites", in, nil); err != nil {
		t.Fatal(err)
	}
	var ch store.GateChange
	if err := c.Do("POST", "/sites/r/gates", map[string]string{"test": "t2"}, &ch); err != nil || ch.Status != store.ChangePending || ch.Gates != (store.Gates{Typecheck: "tc", Lint: "lint", Test: "t2"}) {
		t.Fatalf("%+v %v", ch, err)
	}
	site := func() store.Site {
		var list []store.Site
		if err := c.Do("GET", "/sites", nil, &list); err != nil || len(list) != 1 {
			t.Fatal(err)
		}
		return list[0]
	}
	if site().Gates.Test != "t" {
		t.Fatal("a proposal changed the gates")
	}
	if err := c.Do("POST", "/sites/r/gates", map[string]string{"test": "t"}, nil); err == nil {
		t.Fatal("proposed the gates it already has")
	}
	if err := c.Do("POST", "/sites/nope/gates", map[string]string{"test": "x"}, nil); err == nil {
		t.Fatal("proposed for an unknown Site")
	}
	var pending []store.GateChange
	if err := c.Do("GET", "/gate-changes?status=pending", nil, &pending); err != nil || len(pending) != 1 {
		t.Fatalf("%+v %v", pending, err)
	}
	if err := c.Do("POST", "/gate-changes/"+ch.ID+"/approve", nil, nil); err != nil {
		t.Fatal(err)
	}
	if site().Gates.Test != "t2" {
		t.Fatalf("%+v", site().Gates)
	}
	if err := c.Do("POST", "/gate-changes/"+ch.ID+"/approve", nil, nil); err == nil {
		t.Fatal("approved twice")
	}
	var rej store.GateChange
	if err := c.Do("POST", "/sites/r/gates", map[string]string{"lint": ""}, &rej); err != nil {
		t.Fatal(err)
	}
	if err := c.Do("POST", "/gate-changes/"+rej.ID+"/reject", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do("POST", "/gate-changes/"+rej.ID+"/approve", nil, nil); err == nil || site().Gates.Lint != "lint" {
		t.Fatal("a rejected change was applied")
	}
}
