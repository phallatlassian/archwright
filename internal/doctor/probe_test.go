package doctor

import (
	"context"
	"os"
	"path/filepath"
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
