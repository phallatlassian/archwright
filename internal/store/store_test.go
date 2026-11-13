package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "archwright.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestJobAndPlanRoundTrip(t *testing.T) {
	s, path := open(t)
	j, err := s.CreateJob("Fix login", "it breaks")
	if err != nil {
		t.Fatal(err)
	}
	steps := []Step{
		{ID: "a", Guild: "build", Title: "do it"},
		{ID: "b", Guild: "review", Title: "check", BlockedBy: []string{"a"}, Checkpoint: true},
	}
	if _, err := s.SetPlan(j.ID, steps); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path) // reopened from the real file
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.GetJob(j.ID)
	if err != nil || got.Title != "Fix login" || got.Description != "it breaks" {
		t.Fatalf("job: %+v %v", got, err)
	}
	p, err := s2.GetPlan(j.ID)
	if err != nil || len(p.Steps) != 2 || p.Steps[1].BlockedBy[0] != "a" || !p.Steps[1].Checkpoint {
		t.Fatalf("plan: %+v %v", p, err)
	}
	evs, _ := s2.Events(j.ID)
	if len(evs) != 2 || evs[0].Type != "job.created" || evs[1].Type != "plan.set" {
		t.Fatalf("events: %+v", evs)
	}
	if _, err := s2.GetJob("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s2.GetPlan("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestPlanValidation(t *testing.T) {
	s, _ := open(t)
	j, _ := s.CreateJob("x", "")
	bad := map[string][]Step{
		"missing fields": {{ID: "a"}},
		"duplicate":      {{ID: "a", Guild: "g", Title: "t"}, {ID: "a", Guild: "g", Title: "t"}},
		"unknown block":  {{ID: "a", Guild: "g", Title: "t", BlockedBy: []string{"zz"}}},
		"cycle":          {{ID: "a", Guild: "g", Title: "t", BlockedBy: []string{"b"}}, {ID: "b", Guild: "g", Title: "t", BlockedBy: []string{"a"}}},
	}
	for name, steps := range bad {
		if _, err := s.SetPlan(j.ID, steps); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := s.SetPlan("missing", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := s.CreateJob("  ", ""); err == nil {
		t.Error("blank title accepted")
	}
}

func TestWALAndPrivate(t *testing.T) {
	s, path := open(t)
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q %v", mode, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode %v", fi.Mode())
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	s, _ := open(t)
	j, _ := s.CreateJob("x", "")
	if _, err := s.db.Exec("UPDATE events SET type = 'x'"); err == nil {
		t.Error("update allowed")
	}
	if _, err := s.db.Exec("DELETE FROM events WHERE job_id = ?", j.ID); err == nil {
		t.Error("delete allowed")
	}
}

func rawSetVersion(t *testing.T, path string, v string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA user_version = " + v); err != nil {
		t.Fatal(err)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	s, path := open(t)
	s.Close()
	rawSetVersion(t, path, "999")
	before, _ := os.ReadFile(path)
	if _, err := Open(path); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("want ErrNewerSchema, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("database modified while refusing")
	}
	if baks, _ := filepath.Glob(path + "*.bak"); len(baks) != 0 {
		t.Errorf("unexpected backup %v", baks)
	}
}

func TestMigratesForwardAfterBackup(t *testing.T) {
	s, path := open(t)
	j, _ := s.CreateJob("keep me", "")
	s.Close()
	if baks, _ := filepath.Glob(path + "*.bak"); len(baks) != 0 {
		t.Fatalf("fresh database must not be backed up: %v", baks)
	}

	// Simulate a newer binary shipping a second migration.
	ms, _ := loadMigrations()
	extra := append(ms, migration{version: ms[len(ms)-1].version + 1, sql: "CREATE TABLE later (x TEXT);"})
	old := loadMigrationsFn
	loadMigrationsFn = func() ([]migration, error) { return extra, nil }
	defer func() { loadMigrationsFn = old }()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	loadMigrationsFn = old
	if v, _ := s2.version(); v != extra[len(extra)-1].version {
		t.Fatalf("version %d", v)
	}
	baks, _ := filepath.Glob(fmt.Sprintf("%s.v%d.*.bak", path, ms[len(ms)-1].version))
	if len(baks) != 1 {
		t.Fatalf("want one backup, got %v", baks)
	}
	b, err := Open(baks[0]) // the backup holds the pre-migration data at the old schema
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.GetJob(j.ID); err != nil {
		t.Errorf("backup lacks job: %v", err)
	}
}

func TestGateChangesApplyOnlyOnApproval(t *testing.T) {
	st, _ := open(t)
	if _, err := st.AddSite(Site{Name: "s", Path: "/s", BaseBranch: "main", ConcurrencyCap: 1, Gates: Gates{Test: "a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProposeGates("s", Gates{Test: "a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("no-op proposal: %v", err)
	}
	if _, err := st.ProposeGates("nope", Gates{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown site: %v", err)
	}
	c, err := st.ProposeGates("s", Gates{Test: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSite("s"); got.Gates.Test != "a" {
		t.Fatal("proposal applied")
	}
	got, err := st.ApproveGateChange(c.ID)
	if err != nil || got.Gates.Test != "b" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := st.ApproveGateChange(c.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("approved twice: %v", err)
	}
	r, _ := st.ProposeGates("s", Gates{Test: "c"})
	if err := st.RejectGateChange(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveGateChange(r.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("approved a rejected change: %v", err)
	}
	if got, _ := st.GetSite("s"); got.Gates.Test != "b" {
		t.Fatalf("%+v", got.Gates)
	}
}

func TestApprovingGateChangeRejectsStalePendingChanges(t *testing.T) {
	st, _ := open(t)
	if _, err := st.AddSite(Site{Name: "s", Path: "/s", BaseBranch: "main", ConcurrencyCap: 1, Gates: Gates{Test: "a"}}); err != nil {
		t.Fatal(err)
	}
	c1, _ := st.ProposeGates("s", Gates{Lint: "x", Test: "a"})
	c2, _ := st.ProposeGates("s", Gates{Lint: "", Test: "y"})
	if _, err := st.ApproveGateChange(c1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveGateChange(c2.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("approved a stale change: %v", err)
	}
	if got, _ := st.GetSite("s"); got.Gates.Lint != "x" || got.Gates.Test != "a" {
		t.Fatalf("%+v", got.Gates)
	}
}

func TestInterruptedGateRunsAreNotReportedAsPassing(t *testing.T) {
	st, _ := open(t)
	job, _ := st.CreateJob("J", "")
	h, err := st.SaveHandback(job.ID, "done", []byte(`{}`), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := st.GateRunFor(h.ID); g.Status != GateRunRunning {
		t.Fatalf("%+v", g)
	}
	if err := st.InterruptGateRuns(); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.GateRunFor(h.ID); g.Status != GateRunNotRun || g.Reason == "" {
		t.Fatalf("%+v", g)
	}
	if err := st.FinishGateRun(GateRun{HandbackID: h.ID, JobID: job.ID, Status: GateRunPassed}); !errors.Is(err, ErrConflict) {
		t.Fatalf("finished twice: %v", err)
	}
}
