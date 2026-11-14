package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// endedJob makes a closed Job with one reported dispatch, ended at t.
func endedJob(t *testing.T, s *Store, title string, ended time.Time) Job {
	t.Helper()
	j, err := s.CreateJob(title, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewPiece(j.ID, "masonry", "p1", []byte(`{"goal":"g"}`), "RAW BRIEF with secrets"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE dispatches SET status = ? WHERE job_id = ?", DispatchReported, j.ID); err != nil {
		t.Fatal(err)
	}
	now := s.now
	s.now = func() time.Time { return ended }
	defer func() { s.now = now }()
	if err := s.CloseJob(j.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestPruneRawLeavesMarkerAndDurableRecords(t *testing.T) {
	s, _ := open(t)
	old := endedJob(t, s, "old", time.Now().Add(-40*24*time.Hour))
	fresh := endedJob(t, s, "fresh", time.Now().Add(-10*24*time.Hour))
	if _, err := s.SetPlan(old.ID, []Step{{ID: "a", Guild: "g", Title: "t"}}); err != nil {
		t.Fatal(err)
	}

	rep, err := s.PruneRaw(DefaultRetention, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Pruned) != 1 || rep.Pruned[0].JobID != old.ID || rep.Pruned[0].Dispatches != 1 || !rep.Compact {
		t.Fatalf("%+v", rep)
	}
	got, _ := s.GetJob(old.ID)
	if got.PurgedAt == "" {
		t.Fatal("pruned Job must carry the marker")
	}
	ds, _ := s.Dispatches(old.ID)
	if len(ds) != 1 || ds[0].Rendered != "" || len(ds[0].Brief) == 0 {
		t.Fatalf("raw content goes, the brief stays: %+v", ds)
	}
	if _, err := s.GetPlan(old.ID); err != nil {
		t.Fatalf("plan must stay: %v", err)
	}
	evs, _ := s.Events(old.ID)
	if last := evs[len(evs)-1]; last.Type != "job.purged" || string(last.Payload) == "" {
		t.Fatalf("%+v", last)
	}
	other, _ := s.GetJob(fresh.ID)
	if other.PurgedAt != "" {
		t.Fatal("a Job inside the window is kept whole")
	}
	// A second pass finds nothing new.
	if rep, _ := s.PruneRaw(DefaultRetention, false); len(rep.Pruned) != 0 || rep.Compact {
		t.Fatalf("%+v", rep)
	}
}

func TestPruneRawDryRunChangesNothing(t *testing.T) {
	s, path := open(t)
	old := endedJob(t, s, "old", time.Now().Add(-40*24*time.Hour))
	before, _ := os.ReadFile(path)
	evsBefore, _ := s.Events(old.ID)
	rep, err := s.PruneRaw(DefaultRetention, true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || len(rep.Pruned) != 1 || rep.Pruned[0].Bytes == 0 || rep.Compact {
		t.Fatalf("%+v", rep)
	}
	got, _ := s.GetJob(old.ID)
	ds, _ := s.Dispatches(old.ID)
	evsAfter, _ := s.Events(old.ID)
	if got.PurgedAt != "" || ds[0].Rendered == "" || len(evsAfter) != len(evsBefore) {
		t.Fatal("a dry run must change nothing")
	}
	if after, _ := os.ReadFile(path); len(after) != len(before) {
		t.Fatal("a dry run must not touch the database file")
	}
}

func TestPruneRawNeverTouchesActiveJobs(t *testing.T) {
	s, _ := open(t)
	open1, _ := s.CreateJob("still open", "")
	s.NewPiece(open1.ID, "masonry", "p1", []byte(`{}`), "RAW")
	s.db.Exec("UPDATE jobs SET created_at = ? WHERE id = ?", time.Now().Add(-90*24*time.Hour).Format(time.RFC3339Nano), open1.ID)

	// Ended long ago, but a run is still active.
	busy := endedJob(t, s, "busy", time.Now().Add(-90*24*time.Hour))
	s.db.Exec("UPDATE dispatches SET status = ? WHERE job_id = ?", DispatchRunning, busy.ID)

	rep, err := s.PruneRaw(DefaultRetention, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Pruned) != 0 || len(rep.Active) != 1 || rep.Active[0] != busy.ID {
		t.Fatalf("%+v", rep)
	}
	for _, id := range []string{open1.ID, busy.ID} {
		if j, _ := s.GetJob(id); j.PurgedAt != "" {
			t.Fatalf("%s was pruned", id)
		}
		if ds, _ := s.Dispatches(id); ds[0].Rendered == "" {
			t.Fatalf("%s lost raw content", id)
		}
	}
}

func TestPruneRawHonoursRetention(t *testing.T) {
	s, _ := open(t)
	j := endedJob(t, s, "j", time.Now().Add(-5*24*time.Hour))
	if rep, _ := s.PruneRaw(7*24*time.Hour, false); len(rep.Pruned) != 0 {
		t.Fatalf("%+v", rep)
	}
	if rep, _ := s.PruneRaw(24*time.Hour, false); len(rep.Pruned) != 1 || rep.Pruned[0].JobID != j.ID {
		t.Fatalf("%+v", rep)
	}
}

func TestBackupsKeepTheThreeMostRecent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archwright.db")
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("%s.v%d.2026010%dT000000.000000000Z.bak", path, 8+i%2, i)
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBackups(path, KeptBackups); err != nil {
		t.Fatal(err)
	}
	left, _ := Backups(path)
	if len(left) != 3 {
		t.Fatalf("%v", left)
	}
	for i, want := range []string{"3", "4", "5"} {
		if !strings.Contains(left[i], "2026010"+want+"T") {
			t.Fatalf("%v", left)
		}
	}
}
