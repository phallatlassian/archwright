package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// bareWithWorktree makes a bare clone and a linked worktree of it, as the Works does.
func bareWithWorktree(t *testing.T) (bare, wt string) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	gitIn(t, root, "init", "--quiet", src)
	gitIn(t, src, "commit", "--allow-empty", "-m", "x", "--quiet")
	bare = filepath.Join(root, "demo.git")
	gitIn(t, root, "clone", "--quiet", "--bare", src, bare)
	wt = filepath.Join(root, "wt")
	gitIn(t, bare, "worktree", "add", "--quiet", "-b", "w", wt)
	real := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
	return real(bare), real(wt)
}

func TestWorktreeWritableRootsForAWorksWorktree(t *testing.T) {
	bare, wt := bareWithWorktree(t)
	roots, err := WorktreeWritableRoots(wt)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(bare, "worktrees", "wt"), filepath.Join(bare, "objects"), filepath.Join(bare, "refs"), filepath.Join(bare, "logs")}
	if strings.Join(roots, "\n") != strings.Join(want, "\n") {
		t.Fatalf("roots = %v\nwant %v", roots, want)
	}
	for _, r := range roots {
		if r == bare || strings.HasSuffix(r, "packed-refs") {
			t.Errorf("root %q is too wide or packed-refs", r)
		}
	}
	if err := Check(Spawn{WritableRoots: roots}); err != nil {
		t.Errorf("the guard refuses what the Works computed: %v", err)
	}
}

func TestWorktreeWritableRootsForAPlainRepo(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "--quiet")
	real, _ := filepath.EvalSymlinks(dir)
	roots, err := WorktreeWritableRoots(dir)
	if err != nil || len(roots) != 1 || roots[0] != filepath.Join(real, ".git") {
		t.Fatalf("roots = %v, err = %v", roots, err)
	}
}

func TestWorktreeWritableRootsFailsOutsideARepo(t *testing.T) {
	if roots, err := WorktreeWritableRoots(t.TempDir()); err == nil || roots != nil {
		t.Fatalf("roots = %v, err = %v", roots, err)
	}
}

func TestGuardAcceptsOnlyCleanAbsoluteRoots(t *testing.T) {
	if err := Check(Spawn{WritableRoots: []string{"/a/b", "/c d/e"}}); err != nil {
		t.Errorf("clean roots refused: %v", err)
	}
	bad := map[string][]string{
		"relative":      {"a/b"},
		"parent":        {"/a/../b"},
		"unclean":       {"/a//b"},
		"trailing":      {"/a/b/"},
		"filesystem":    {"/"},
		"quote":         {`/a"b`},
		"backslash":     {`/a\b`},
		"comma":         {"/a,/b"},
		"array":         {"/a]"},
		"equals":        {"/a=b"},
		"newline":       {"/a\nb"},
		"too many":      {"/1", "/2", "/3", "/4", "/5", "/6", "/7", "/8", "/9"},
		"empty element": {""},
	}
	for name, roots := range bad {
		if err := Check(Spawn{WritableRoots: roots}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Check(Spawn{Mode: "plan", WritableRoots: []string{"/a"}}); !errors.Is(err, ErrForbidden) {
		t.Errorf("plan mode with roots: %v", err)
	}
}

// Only the Works' own Spawn.WritableRoots can carry the sandbox: every
// hand-built spelling of it, even for the allowed key and value, is refused.
func TestGuildOrAgentSuppliedSandboxFlagsAreStillRefused(t *testing.T) {
	roots := `sandbox_workspace_write.writable_roots=["/a"]`
	for name, args := range map[string][]string{
		"hand-built -c":           {"-c", roots},
		"hand-built --config":     {"--config", roots},
		"hand-built --config=":    {"--config=" + roots},
		"-c glued":                {"-c" + roots},
		"-s workspace-write":      {"-s", "workspace-write"},
		"--sandbox":               {"--sandbox", "workspace-write"},
		"--sandbox=":              {"--sandbox=workspace-write"},
		"-s glued":                {"-sworkspace-write"},
		"read-only is a -s too":   {"-s", "read-only"},
		"full access":             {"-s", "danger-full-access"},
		"full access value":       {"--sandbox=danger-full-access"},
		"add-dir":                 {"--add-dir", "/"},
		"other -c key":            {"-c", "sandbox_mode=workspace-write"},
		"approval policy -c":      {"-c", "approval_policy=never"},
		"approval flag":           {"-a", "never"},
		"approval flag long":      {"--ask-for-approval", "never"},
		"network access":          {"-c", "sandbox_workspace_write.network_access=true"},
		"roots plus other config": {"--config", "sandbox_workspace_write.writable_roots=[\"/\"]"},
	} {
		if err := Check(Spawn{ExtraArgs: args}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: %v", name, err)
		}
		if err := Check(Spawn{WritableRoots: []string{"/a"}, ExtraArgs: args}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s alongside works roots: %v", name, err)
		}
	}
}

func codexRoots(t *testing.T, req StartRequest) (string, []Event) {
	t.Helper()
	run, dir, err := codexStubRun(t, "codex-success.jsonl", req)
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, run)
	run.Result()
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	return string(args), evs
}

func TestCodexWrightStepGetsWorkspaceWriteAndTheOneRootsKey(t *testing.T) {
	roots := []string{"/clone/worktrees/w", "/clone/objects", "/clone/refs", "/clone/logs"}
	args, evs := codexRoots(t, StartRequest{Brief: "b", Spawn: Spawn{WritableRoots: roots}})
	want := "exec\n--json\n--ephemeral\n-s\nworkspace-write\n-c\n" +
		`sandbox_workspace_write.writable_roots=["/clone/worktrees/w","/clone/objects","/clone/refs","/clone/logs"]` + "\n--\nb\n"
	if args != want {
		t.Errorf("args = %q\nwant %q", args, want)
	}
	if strings.Count(args, "-c\n") != 1 || strings.Count(args, "-s\n") != 1 {
		t.Errorf("more than the one -s and -c: %q", args)
	}
	p := evs[0].Posture
	if p.Sandbox != SandboxWorkspaceWrite || strings.Join(p.WritableRoots, ",") != strings.Join(roots, ",") || !strings.Contains(strings.Join(p.Flags, " "), "workspace-write") {
		t.Errorf("posture = %+v", p)
	}
}

func TestCodexStepWithoutRootsKeepsTheRunnerDefault(t *testing.T) {
	args, evs := codexRoots(t, StartRequest{Brief: "b"})
	if args != "exec\n--json\n--ephemeral\n--\nb\n" {
		t.Errorf("args = %q", args)
	}
	if p := evs[0].Posture; p.Sandbox != SandboxInherited || len(p.WritableRoots) != 0 {
		t.Errorf("posture = %+v", p)
	}
}

func TestCodexRefusesUnvalidatedRootsBeforeSpawn(t *testing.T) {
	run, dir, err := codexStubRun(t, "codex-success.jsonl", StartRequest{Brief: "b", Spawn: Spawn{WritableRoots: []string{"/"}}})
	if !errors.Is(err, ErrForbidden) || run != nil {
		t.Fatalf("run = %v, err = %v", run, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "args")); err == nil {
		t.Error("codex was spawned")
	}
}

func TestClaudeIgnoresWritableRoots(t *testing.T) {
	dir := t.TempDir()
	stub, _ := filepath.Abs("testdata/stub-claude.sh")
	run, err := (&Claude{Binary: stub, Grace: time.Second}).Start(context.Background(), StartRequest{Brief: "b", Dir: dir,
		Env:   map[string]string{"ARCHWRIGHT_STUB_FIXTURE": "success.jsonl", "ARCHWRIGHT_STUB_ARGS": filepath.Join(dir, "args")},
		Spawn: Spawn{WritableRoots: []string{"/clone/refs"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run)
	run.Result()
	b, _ := os.ReadFile(filepath.Join(dir, "args"))
	st := string(b)
	if st == "" || strings.Contains(st, "workspace-write") || strings.Contains(st, "writable_roots") || strings.Contains(st, "-c\n") {
		t.Errorf("claude args = %q", st)
	}
}

func TestCodexManagedPolicyDisallowingWorkspaceWriteFailsTheRunClosed(t *testing.T) {
	run, _, err := codexStubRun(t, "codex-sandbox-disallowed.jsonl", StartRequest{Brief: "b", Spawn: Spawn{WritableRoots: []string{"/clone/refs"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run)
	res := run.Result()
	if res.Status != StatusFailed || !strings.HasPrefix(res.Error, WorkspaceWriteDisallowed) {
		t.Errorf("result = %+v", res)
	}
}

func TestCodexSandboxNoticeIsOnlyAFailureWhenWriteWasAsked(t *testing.T) {
	run, _, err := codexStubRun(t, "codex-sandbox-disallowed.jsonl", StartRequest{Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run)
	if res := run.Result(); strings.Contains(res.Error, WorkspaceWriteDisallowed) {
		t.Errorf("a read-only step failed on a write notice: %+v", res)
	}
}
