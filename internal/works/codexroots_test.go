package works

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/runner"
)

// codexRootsRig runs the real AgentRunner on the stub codex, which records its argv.
type codexRootsRig struct {
	t    *testing.T
	args string
	ar   *AgentRunner
}

func newCodexRootsRig(t *testing.T, g Guild) *codexRootsRig {
	t.Helper()
	td, _ := filepath.Abs("../runner/testdata")
	r := &codexRootsRig{t: t, args: filepath.Join(t.TempDir(), "args")}
	t.Setenv("ARCHWRIGHT_STUB_ARGS", r.args)
	t.Setenv("ARCHWRIGHT_STUB_FIXTURE", "codex-success.jsonl")
	r.ar = &AgentRunner{
		Runners: map[string]runner.Runner{"codex": &runner.Codex{Binary: filepath.Join(td, "stub-codex.sh")}},
		Guilds:  func(string) (Guild, error) { return g, nil },
	}
	return r
}

func (r *codexRootsRig) argv() string {
	b, _ := os.ReadFile(r.args)
	return string(b)
}

func (r *codexRootsRig) clear() { os.Remove(r.args) }

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var codexWright = Guild{Name: "carvers", Binding: defs.Binding{Guild: "carvers", Runner: "codex", Access: defs.ReadWrite}}

func TestCodexWrightInAWorksWorktreeGetsTheGitdirAndSharedRefRoots(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "--quiet", "src")
	git(t, filepath.Join(root, "src"), "commit", "--allow-empty", "-m", "x", "--quiet")
	git(t, root, "clone", "--quiet", "--bare", "src", "demo.git")
	git(t, filepath.Join(root, "demo.git"), "worktree", "add", "--quiet", "-b", "w", filepath.Join(root, "wt"))
	bare, wt := realPath(t, filepath.Join(root, "demo.git")), realPath(t, filepath.Join(root, "wt"))

	rig := newCodexRootsRig(t, codexWright)
	if err := rig.ar.Run(context.Background(), RunRequest{Guild: "carvers", Worktree: wt, Brief: "b"}); err != nil {
		t.Fatal(err)
	}
	want := `-s
workspace-write
-c
sandbox_workspace_write.writable_roots=["` + strings.Join([]string{bare + "/worktrees/wt", bare + "/objects", bare + "/refs", bare + "/logs"}, `","`) + `"]
--
b
`
	if got := rig.argv(); !strings.Contains(got, want) {
		t.Errorf("argv = %q\nwant it to contain %q", got, want)
	}
}

func TestCodexWrightInAPlainRepoGetsItsDotGit(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "--quiet")
	rig := newCodexRootsRig(t, codexWright)
	if err := rig.ar.Run(context.Background(), RunRequest{Guild: "carvers", Worktree: dir, Brief: "b"}); err != nil {
		t.Fatal(err)
	}
	want := `-s
workspace-write
-c
sandbox_workspace_write.writable_roots=["` + realPath(t, dir) + `/.git"]
`
	if got := rig.argv(); !strings.Contains(got, want) {
		t.Errorf("argv = %q", got)
	}
}

func TestCodexWardenReviewAndReadOnlyGuildStepsGetNoSandboxFlags(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "--quiet")
	check := func(name string, rig *codexRootsRig, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := rig.argv(); got != "exec\n--json\n--ephemeral\n--\nb\n" {
			t.Errorf("%s argv = %q", name, got)
		}
	}
	rig := newCodexRootsRig(t, codexWright)
	w := &AgentWarden{Agent: rig.ar}
	check("warden", rig, w.RunWarden(context.Background(), WardenRequest{Guild: "carvers", Worktree: dir, Brief: "b"}))
	rig.clear()
	check("review", rig, w.RunReview(context.Background(), ReviewRequest{Worktree: dir, Brief: "b"}))

	ro := codexWright
	ro.Binding.Access = defs.ReadOnly
	rig = newCodexRootsRig(t, ro)
	// A read-only Guild asks for plan mode, which codex cannot honour: refused
	// before spawn, and in any case never given a write sandbox.
	if err := rig.ar.Run(context.Background(), RunRequest{Guild: "carvers", Worktree: dir, Brief: "b"}); err == nil {
		t.Error("read-only codex Wright was started")
	}
	if strings.Contains(rig.argv(), "workspace-write") {
		t.Errorf("argv = %q", rig.argv())
	}
}

func TestCodexWrightFailsTheBindWhenRootsCannotBeComputed(t *testing.T) {
	rig := newCodexRootsRig(t, codexWright)
	err := rig.ar.Run(context.Background(), RunRequest{Guild: "carvers", Worktree: t.TempDir(), Brief: "b"})
	if err == nil || !strings.Contains(err.Error(), "writable roots") {
		t.Fatalf("err = %v", err)
	}
	if rig.argv() != "" {
		t.Error("codex was spawned without roots")
	}
}

func TestClaudeWrightGetsNoWritableRoots(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "--quiet")
	td, _ := filepath.Abs("../runner/testdata")
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("ARCHWRIGHT_STUB_ARGS", args)
	t.Setenv("ARCHWRIGHT_STUB_FIXTURE", "success.jsonl")
	ar := &AgentRunner{
		Runners: map[string]runner.Runner{"claude": &runner.Claude{Binary: filepath.Join(td, "stub-claude.sh")}},
		Guilds:  func(string) (Guild, error) { return agentGuilds["masonry"], nil },
	}
	if err := ar.Run(context.Background(), RunRequest{Guild: "masonry", Worktree: dir, Brief: "b"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(args)
	if strings.Contains(string(b), "workspace-write") || strings.Contains(string(b), "writable_roots") {
		t.Errorf("argv = %q", b)
	}
}
