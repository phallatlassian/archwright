package forge

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

const integration = "archwright/job-1/integration"

func req(clone string) PushRequest {
	return PushRequest{SiteURL: "https://github.com/acme/app.git", ClonePath: clone, Branch: integration, DefaultBranch: "main"}
}

func TestPushArgsPushesOnlyTheNamedBranchToOriginUnforced(t *testing.T) {
	args, err := PushArgs(req("/clones/app"))
	if err != nil {
		t.Fatal(err)
	}
	want := "-C /clones/app push --porcelain --no-verify origin refs/heads/" + integration + ":refs/heads/" + integration
	if got := strings.Join(args, " "); got != want {
		t.Fatalf("argv\n got %s\nwant %s", got, want)
	}
	for _, a := range args {
		if a == "--force" || a == "-f" || strings.HasPrefix(a, "--force-") || strings.HasPrefix(a, "+") || strings.Contains(a, ":+") || a == "--delete" || a == "--mirror" || a == "--all" {
			t.Fatalf("argv carries %q", a)
		}
	}
}

func TestPushArgsRefusesWhatWouldForceOrReachTheDefaultBranch(t *testing.T) {
	for name, mutate := range map[string]func(*PushRequest){
		"default branch":             func(r *PushRequest) { r.Branch = "main" },
		"default branch as full ref": func(r *PushRequest) { r.DefaultBranch, r.Branch = "refs/heads/main", "main" },
		"unknown default branch":     func(r *PushRequest) { r.DefaultBranch = "" },
		"no branch":                  func(r *PushRequest) { r.Branch = "" },
		"no clone":                   func(r *PushRequest) { r.ClonePath = "" },
		"forced refspec":             func(r *PushRequest) { r.Branch = "+" + integration },
		"option":                     func(r *PushRequest) { r.Branch = "--force" },
		"delete refspec":             func(r *PushRequest) { r.Branch = ":" + integration },
		"retargeted refspec":         func(r *PushRequest) { r.Branch = integration + ":refs/heads/main" },
		"full ref":                   func(r *PushRequest) { r.Branch = "refs/heads/" + integration },
		"HEAD":                       func(r *PushRequest) { r.Branch = "HEAD" },
		"parent traversal":           func(r *PushRequest) { r.Branch = "a/../main" },
		"glob":                       func(r *PushRequest) { r.Branch = "archwright/*" },
		"whitespace":                 func(r *PushRequest) { r.Branch = integration + " main" },
		"trailing slash":             func(r *PushRequest) { r.Branch = integration + "/" },
		"lock suffix":                func(r *PushRequest) { r.Branch = integration + ".lock" },
		"reflog syntax":              func(r *PushRequest) { r.Branch = "a@{1}" },
	} {
		r := req("/clones/app")
		mutate(&r)
		if args, err := PushArgs(r); err == nil {
			t.Errorf("%s: built %v", name, args)
		}
	}
}

// stubGit installs the stub git as `git` first on PATH and returns its call log.
func stubGit(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	src, err := os.ReadFile("testdata/stub-git.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "git"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls.log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_GIT_MODE", mode)
	t.Setenv("STUB_GIT_LOG", log)
	return log
}

func TestGHPushRunsExactlyTheBuiltArgvOnce(t *testing.T) {
	log := stubGit(t, "ok")
	r := req("/clones/app")
	if err := (GH{}).PushBranch(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	want, _ := PushArgs(r)
	if calls, _ := os.ReadFile(log); strings.TrimSpace(string(calls)) != strings.Join(want, " ") {
		t.Fatalf("git called as %q, want %q", calls, strings.Join(want, " "))
	}
}

func TestGHPushRefusalsNeverReachGit(t *testing.T) {
	log := stubGit(t, "ok")
	r := req("/clones/app")
	r.Branch = "main"
	var fe *Error
	if err := (GH{}).PushBranch(context.Background(), r); !errors.As(err, &fe) || fe.Kind != KindOther || !strings.Contains(fe.Detail, "default branch") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("git ran for a refused push")
	}
}

func TestGHPushClassifiesFaultsAndDoesNotRetry(t *testing.T) {
	for _, c := range []struct{ mode, kind, detail string }{
		{"auth", KindAuth, ""}, {"network", KindNetwork, ""}, {"rejected", KindOther, "non-fast-forward"},
	} {
		log := stubGit(t, c.mode)
		err := (GH{}).PushBranch(context.Background(), req("/clones/app"))
		var fe *Error
		if !errors.As(err, &fe) || fe.Kind != c.kind || !strings.Contains(fe.Detail, c.detail) {
			t.Fatalf("%s: %v", c.mode, err)
		}
		if calls, _ := os.ReadFile(log); strings.Count(string(calls), "\n") != 1 {
			t.Fatalf("%s: git called more than once: %q", c.mode, calls)
		}
	}
}

func TestGHPushWithoutGitIsAnOtherFault(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var fe *Error
	if err := (GH{}).PushBranch(context.Background(), req("/clones/app")); !errors.As(err, &fe) || fe.Kind != KindOther || !strings.Contains(fe.Detail, "git is not installed") {
		t.Fatalf("%v", err)
	}
}

func TestByHostPushRoutesByTheSitesHost(t *testing.T) {
	log := stubGit(t, "ok")
	f := Default()
	for _, u := range []string{"https://github.com/acme/app.git", "git@github.com:acme/app.git", "ssh://git@GitHub.com/acme/app"} {
		r := req("/clones/app")
		r.SiteURL = u
		if err := f.PushBranch(context.Background(), r); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"https://gitlab.example.com/acme/app", "/local/path", "", "::bad"} {
		r := req("/clones/app")
		r.SiteURL = u
		if err := f.PushBranch(context.Background(), r); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%q: %v", u, err)
		}
	}
	if calls, _ := os.ReadFile(log); strings.Count(string(calls), "\n") != 3 {
		t.Fatalf("git calls: %q", calls)
	}
	if err := (Unsupported{}).PushBranch(context.Background(), req("/c")); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// localRemote is a bare repository standing in for the Forge's remote, and a
// bare clone of it like the Works keeps. Nothing here touches a real remote.
func localRemote(t *testing.T) (bare, clone string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	bare, clone, seed := filepath.Join(root, "remote.git"), filepath.Join(root, "clone.git"), filepath.Join(root, "seed")
	for _, args := range [][]string{{"init", "--bare", "-b", "main", bare}, {"init", "-b", "main", seed}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	os.WriteFile(filepath.Join(seed, "f"), []byte("0\n"), 0o644)
	run(t, seed, "add", ".")
	run(t, seed, "commit", "-m", "init")
	run(t, seed, "push", bare, "main")
	if out, err := exec.Command("git", "clone", "--bare", bare, clone).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	run(t, clone, "branch", integration, "main")
	return bare, clone
}

func commitOn(t *testing.T, clone, branch, name string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, clone, "worktree", "add", wt, branch)
	defer run(t, clone, "worktree", "remove", "--force", wt)
	os.WriteFile(filepath.Join(wt, name), []byte(name), 0o644)
	run(t, wt, "add", ".")
	run(t, wt, "commit", "-m", name)
	return run(t, wt, "rev-parse", "HEAD")
}

func TestPushToALocalBareRemoteIsFastForwardOnlyAndSparesTheDefaultBranch(t *testing.T) {
	bare, clone := localRemote(t)
	mainBefore := run(t, bare, "rev-parse", "main")
	r := req(clone)
	ctx := context.Background()

	first := commitOn(t, clone, integration, "a")
	if err := (GH{}).PushBranch(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := run(t, bare, "rev-parse", integration); got != first {
		t.Fatalf("remote has %s, want %s", got, first)
	}

	second := commitOn(t, clone, integration, "b") // a fast-forward re-push
	if err := (GH{}).PushBranch(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := run(t, bare, "rev-parse", integration); got != second {
		t.Fatalf("remote has %s, want %s", got, second)
	}

	// Rewrite history locally: the remote must refuse it, since the push is never forced.
	run(t, clone, "branch", "-f", integration, "main")
	rewritten := commitOn(t, clone, integration, "c")
	err := (GH{}).PushBranch(ctx, r)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindOther || !strings.Contains(fe.Detail, "non-fast-forward") {
		t.Fatalf("a rewritten branch was not refused as non-fast-forward: %v", err)
	}
	if got := run(t, bare, "rev-parse", integration); got != second || got == rewritten {
		t.Fatalf("the remote branch moved to %s", got)
	}

	// Config that would force or retarget pushes does not reach an explicit refspec.
	run(t, clone, "config", "remote.origin.push", "+refs/heads/*:refs/heads/*")
	run(t, clone, "config", "push.default", "matching")
	run(t, clone, "branch", "-f", "main", rewritten)
	if err := (GH{}).PushBranch(ctx, r); err == nil {
		t.Fatal("a non-fast-forward push succeeded under a forcing remote.origin.push")
	}
	if got := run(t, bare, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("the default branch moved: %s -> %s", mainBefore, got)
	}

	// The default branch is refused before git runs, even when it has new commits.
	d := req(clone)
	d.Branch = "main"
	if err := (GH{}).PushBranch(ctx, d); err == nil {
		t.Fatal("the default branch was pushed")
	}
	if got := run(t, bare, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("the default branch moved: %s -> %s", mainBefore, got)
	}
}

func TestPushTimesOutAsANetworkFault(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nsleep 60\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var fe *Error
	if err := (GH{Timeout: 100 * time.Millisecond}).PushBranch(context.Background(), req("/c")); !errors.As(err, &fe) || fe.Kind != KindNetwork {
		t.Fatalf("%v", err)
	}
}
