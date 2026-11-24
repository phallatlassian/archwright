package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A Session's hook reports to the Works and never answers the CLI: no output,
// exit 0 whatever happens, so it can neither allow nor deny anything and never fails the Session.
func TestSessionHookReportsThroughTheSpoolFileAndPrintsNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hooks.jsonl")
	run := func(stdin string) (string, error) {
		c := exec.Command(binary, "session-hook")
		c.Env = append(os.Environ(), "ARCHWRIGHT_RUN=run-1", "ARCHWRIGHT_HOOK_TOKEN=tok", "ARCHWRIGHT_HOOK_FILE="+file, "ARCHWRIGHT_HOOK_SOCKET=/nonexistent/h.sock")
		c.Stdin = strings.NewReader(stdin)
		out, err := c.CombinedOutput()
		return string(out), err
	}
	for _, payload := range []string{`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`, "garbage, not JSON"} {
		if out, err := run(payload); err != nil || out != "" {
			t.Fatalf("payload %q: output %q, err %v", payload, out, err)
		}
	}
	b, _ := os.ReadFile(file)
	if got := string(b); strings.Count(got, "\n") != 1 || !strings.Contains(got, "PermissionRequest") || !strings.Contains(got, `"run":"run-1"`) {
		t.Fatalf("spool = %q", got)
	}
}
