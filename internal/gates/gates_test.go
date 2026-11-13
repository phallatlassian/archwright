package gates

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

func TestRunsEveryGateInOrderInTheDirectory(t *testing.T) {
	dir := t.TempDir()
	res := Run(context.Background(), dir, store.Gates{Typecheck: "pwd", Lint: "echo bad >&2; exit 3", Test: "echo ok"}, 0)
	if len(res) != 3 || res[0].Gate != "typecheck" || res[1].Gate != "lint" || res[2].Gate != "test" {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res[0].Output, filepath.Base(dir)) || res[0].Status != store.GatePassed {
		t.Errorf("typecheck: %+v", res[0])
	}
	if res[1].Status != store.GateFailed || res[1].ExitCode != 3 || !strings.Contains(res[1].Output, "bad") {
		t.Errorf("lint: %+v", res[1])
	}
	if res[2].Status != store.GatePassed { // a failed gate does not stop the rest
		t.Errorf("test: %+v", res[2])
	}
	if s, _ := Summarize(res); s != store.GateRunFailed {
		t.Errorf("summary %s", s)
	}
}

func TestSkippedGatesAndSummary(t *testing.T) {
	res := Run(context.Background(), t.TempDir(), store.Gates{Test: "true"}, 0)
	if res[0].Status != store.GateSkipped || res[2].Status != store.GatePassed {
		t.Fatalf("%+v", res)
	}
	if s, _ := Summarize(res); s != store.GateRunPassed {
		t.Errorf("summary %s", s)
	}
	if s, reason := Summarize(Run(context.Background(), t.TempDir(), store.Gates{}, 0)); s != store.GateRunNotRun || reason == "" {
		t.Errorf("no gates configured: %s %q", s, reason)
	}
}

func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survivor")
	start := time.Now()
	res := Run(context.Background(), t.TempDir(), store.Gates{Test: "(sleep 1; touch " + marker + ") & sleep 30"}, 200*time.Millisecond)
	if res[2].Status != store.GateFailed || !strings.Contains(res[2].Output, "timed out") {
		t.Fatalf("%+v", res[2])
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("did not stop promptly")
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a child outlived the gate")
	}
}

func TestOutputKeepsTheTail(t *testing.T) {
	res := Run(context.Background(), t.TempDir(), store.Gates{Test: "yes line | head -n 20000; echo THE-END"}, 0)
	if len(res[2].Output) > maxOutput+10 || !strings.HasSuffix(strings.TrimSpace(res[2].Output), "THE-END") {
		t.Fatalf("%d bytes, tail %q", len(res[2].Output), res[2].Output[max(0, len(res[2].Output)-20):])
	}
}
