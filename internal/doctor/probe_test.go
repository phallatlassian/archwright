package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// probeStub probes the stand-in claude script (never a real runner) in the given mode.
func probeStub(t *testing.T, mode string) ProbeResult {
	t.Helper()
	t.Setenv("STUB_MODE", mode)
	uid := 90000 + os.Getpid()%1000 // a spool root of its own, so nothing real is touched
	root := filepath.Join("/tmp", "claude-"+itoa(uid))
	t.Cleanup(func() { os.RemoveAll(root) })
	stub, err := filepath.Abs("testdata/stub-claude.sh")
	if err != nil {
		t.Fatal(err)
	}
	return RunnerProber{UID: uid, TempDir: t.TempDir(), Timeout: 20 * time.Second}.Probe(context.Background(), "claude", stub)
}

func itoa(n int) string {
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestProbeSeesTheCallAndTheResponse(t *testing.T) {
	r := probeStub(t, "ok")
	if r.Err != "" || len(r.Denied) != 0 || !r.RequestSeen || !r.ResponseRead {
		t.Fatalf("%+v", r)
	}
}

func TestProbeReportsADeniedCall(t *testing.T) {
	r := probeStub(t, "deny")
	if len(r.Denied) != 1 || r.Denied[0].Tool != "Bash" || r.RequestSeen {
		t.Fatalf("%+v", r)
	}
}

func TestProbeWithNoCallLeavesNoRequest(t *testing.T) {
	r := probeStub(t, "silent")
	if r.Err != "" || r.RequestSeen || r.ResponseRead || len(r.Denied) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestProbeRemovesItsSpool(t *testing.T) {
	uid := 91000 + os.Getpid()%1000
	root := filepath.Join("/tmp", "claude-"+itoa(uid), "archwright")
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(root)) })
	t.Setenv("STUB_MODE", "ok")
	stub, _ := filepath.Abs("testdata/stub-claude.sh")
	RunnerProber{UID: uid, TempDir: t.TempDir(), Timeout: 20 * time.Second}.Probe(context.Background(), "claude", stub)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("the probe left its spool root behind: %v", err)
	}
}

// A real codex refuses to run outside a git repository, so the probe must run
// in one (it must not widen codex's posture with --skip-git-repo-check).
func TestProbeRunsCodexInAGitDirectory(t *testing.T) {
	t.Setenv("STUB_MODE", "ok")
	t.Setenv("STUB_NEEDS_GIT", "1")
	uid := 92000 + os.Getpid()%1000
	t.Cleanup(func() { os.RemoveAll(filepath.Join("/tmp", "claude-"+itoa(uid))) })
	stub, _ := filepath.Abs("testdata/stub-codex.sh")
	r := RunnerProber{UID: uid, TempDir: t.TempDir(), Timeout: 20 * time.Second}.Probe(context.Background(), "codex", stub)
	if r.Err != "" || !r.RequestSeen || !r.ResponseRead {
		t.Fatalf("%+v", r)
	}
}

func probeCodexStub(t *testing.T, mode string) (ProbeResult, string) {
	t.Helper()
	t.Setenv("STUB_MODE", mode)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("STUB_ARGS", args)
	uid := 93000 + os.Getpid()%1000
	t.Cleanup(func() { os.RemoveAll(filepath.Join("/tmp", "claude-"+itoa(uid))) })
	stub, _ := filepath.Abs("testdata/stub-codex.sh")
	r := RunnerProber{UID: uid, TempDir: t.TempDir(), Timeout: 20 * time.Second}.Probe(context.Background(), "codex", stub)
	b, _ := os.ReadFile(args)
	return r, string(b)
}

// The codex probe runs under the posture a Codex Wright runs under (ADR 0023).
func TestProbeRunsCodexUnderWorkspaceWriteWithItsOwnGitdirRoot(t *testing.T) {
	r, args := probeCodexStub(t, "ok")
	if r.Err != "" || r.WriteDisallowed || !r.ResponseRead {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(args, "-s\nworkspace-write\n-c\nsandbox_workspace_write.writable_roots=[\"") || !strings.Contains(args, ".git\"]\n") {
		t.Errorf("args = %q", args)
	}
}

func TestProbeReportsManagedPolicyDisallowingWorkspaceWrite(t *testing.T) {
	r, _ := probeCodexStub(t, "nowrite")
	if !r.WriteDisallowed || r.RequestSeen || len(r.Notices) != 1 || !strings.Contains(r.Notices[0], "sandbox_mode") {
		t.Fatalf("%+v", r)
	}
}
