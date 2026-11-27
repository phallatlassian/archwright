package works

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/store"
)

func TestMigrateClonesEnabledOnlyByTheFlag(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "1": true, "true": true, "no": false} {
		got := MigrateClonesEnabled(func(k string) string {
			if k == EnvMigrateClones {
				return v
			}
			return ""
		})
		if got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
}

// legacyRig is a Site that still has a bare clone, a Runs over it and a probe.
type legacyRig struct {
	st    *store.Store
	sm    *sites.Manager
	runs  *Runs
	probe *probeRig
	bare  string
}

func newLegacyRig(t *testing.T) *legacyRig {
	t.Helper()
	p := newProbeRig(t)
	site, err := p.st.GetSite("demo")
	if err != nil {
		t.Fatal(err)
	}
	// Turn the registered Site into one with the pre-ADR-0030 bare clone.
	bare := filepath.Join(filepath.Dir(filepath.Dir(site.ClonePath)), "demo.git")
	if out, err := exec.Command("git", "clone", "--bare", "--quiet", site.ClonePath, bare).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := p.st.SetClonePath("demo", bare); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(site.ClonePath)); err != nil {
		t.Fatal(err)
	}
	runs := NewRuns(p.st)
	t.Cleanup(runs.Close)
	runs.UseSites(p.sm)
	runs.UseTrustProbe(p.p)
	return &legacyRig{st: p.st, sm: p.sm, runs: runs, probe: p, bare: bare}
}

func TestMigrateClonesReplacesABareCloneAndProbesItsMainCheckout(t *testing.T) {
	r := newLegacyRig(t)
	if got := r.runs.MigrateClones(); len(got) != 1 || got[0] != "demo" {
		t.Fatalf("migrated %v", got)
	}
	s, _ := r.st.GetSite("demo")
	if filepath.Base(s.ClonePath) != "main" {
		t.Fatalf("clone path %s", s.ClonePath)
	}
	if _, err := os.Stat(r.bare); !os.IsNotExist(err) {
		t.Error("bare clone kept")
	}
	// The probe ran, in the main checkout.
	deadline := time.Now().Add(5 * time.Second)
	for r.probe.trust("claude").State != store.TrustCaptured && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := r.probe.trust("claude"); got.State != store.TrustCaptured {
		t.Fatalf("claude: %+v", got)
	}
	if l := r.probe.claude.Launches(); len(l) != 1 || l[0].Dir != s.ClonePath {
		t.Errorf("probe launches %+v, want one in %s", l, s.ClonePath)
	}
	if items, _ := r.st.NeedsYou(); len(items) != 0 {
		t.Errorf("needs-you items: %+v", items)
	}
	// Migrated, a Site is left alone.
	if got := r.runs.MigrateClones(); len(got) != 0 {
		t.Errorf("migrated again: %v", got)
	}
}

func TestMigrateClonesWithActiveJobsFilesANeedsYouItemAndKeepsTheBareClone(t *testing.T) {
	r := newLegacyRig(t)
	j, err := r.st.CreateJobOn("demo", "running", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.runs.MigrateClones(); len(got) != 0 {
		t.Fatalf("migrated %v with an active Job", got)
	}
	r.runs.MigrateClones() // again: no second item
	s, _ := r.st.GetSite("demo")
	if s.ClonePath != r.bare {
		t.Errorf("clone path moved to %s", s.ClonePath)
	}
	items, _ := r.st.NeedsYou()
	if len(items) != 1 || items[0].Kind != store.NeedsCloneMigration || items[0].Site != "demo" {
		t.Fatalf("items %+v", items)
	}
	if l := r.probe.claude.Launches(); len(l) != 0 {
		t.Errorf("probed while waiting: %+v", l)
	}
	// Once the Job closes the next start migrates it and clears the item.
	if err := r.st.CloseJob(j.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
	if got := r.runs.MigrateClones(); len(got) != 1 {
		t.Fatalf("migrated %v", got)
	}
	if items, _ := r.st.NeedsYou(); len(items) != 0 {
		t.Errorf("the waiting item stayed: %+v", items)
	}
}
