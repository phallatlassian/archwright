package works

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/runner"
)

// A denied action in a recorded Claude stream ends up in the Needs-you inbox
// as permission wanted, with the exact command, once however often it is raised.
func TestPermissionDeniedSurfacesAsPermissionWanted(t *testing.T) {
	_, st := newRuns(t)
	job, err := st.CreateJob("build it", "")
	if err != nil {
		t.Fatal(err)
	}
	stub, _ := filepath.Abs("../runner/testdata/stub-claude.sh")
	c := &runner.Claude{Binary: stub}
	run, err := c.Start(context.Background(), runner.StartRequest{Brief: "b", Dir: t.TempDir(), Env: map[string]string{"STUB_FIXTURE": "denied.jsonl"}})
	if err != nil {
		t.Fatal(err)
	}
	denials := run.Result().Denials

	for i := 0; i < 2; i++ {
		n, err := RaisePermissionsWanted(st, job.ID, "", denials)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[int]int{0: 2, 1: 0}[i]; n != want {
			t.Fatalf("raise %d added %d, want %d", i, n, want)
		}
	}
	items, _ := st.NeedsYou()
	if len(items) != 2 || items[0].Kind != "permission-wanted" || !strings.Contains(items[0].Summary, "rm -rf build && make dist") {
		t.Fatalf("inbox = %+v", items)
	}
}
