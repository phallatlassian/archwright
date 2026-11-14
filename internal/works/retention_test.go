package works

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/store"
)

func TestReadRetention(t *testing.T) {
	cases := []struct {
		file    string
		want    time.Duration
		wantErr bool
	}{
		{"", store.DefaultRetention, false},
		{"runner = \"claude\"\n", store.DefaultRetention, false},
		{"retention_days = 7 # a week\n", 7 * 24 * time.Hour, false},
		{"retention_days = \"90\"\n", 90 * 24 * time.Hour, false},
		{"retention_days = 0\n", 0, true},
		{"retention_days = soon\n", 0, true},
	}
	for _, c := range cases {
		d := paths.Dirs{Config: t.TempDir()}
		if c.file != "" {
			if err := os.WriteFile(filepath.Join(d.Config, "config.toml"), []byte(c.file), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got, err := ReadRetention(d)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%q: got %v, %v", c.file, got, err)
		}
	}
}

func TestPruneRouteDryRunsAndReportsActiveRuns(t *testing.T) {
	d := newDash(t, Limits{})
	d.send("a")
	active := func() int {
		code, body := d.do("GET", "/runs/active", "", nil)
		var out map[string]int
		if code != 200 || json.Unmarshal([]byte(body), &out) != nil {
			t.Fatalf("%d %s", code, body)
		}
		return out["active"]
	}
	if n := active(); n != 1 {
		t.Fatalf("a Wright is running: %d", n)
	}
	var rep store.PruneReport
	if err := json.Unmarshal([]byte(d.post("/prune", `{"dry_run":true}`)), &rep); err != nil || !rep.DryRun || len(rep.Pruned) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	d.h.release("a")
	d.waitReported("a")
	if n := active(); n != 0 {
		t.Fatalf("nothing is running: %d", n)
	}
	// A Job still open is never pruned, however short the retention.
	d.post("/prune", `{"days":1}`)
	if dis, _ := d.st.LatestDispatch(d.job.ID, "a"); dis.Rendered == "" {
		t.Fatal("an open Job lost its raw content")
	}
}
