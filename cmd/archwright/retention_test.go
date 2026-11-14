package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// seedJob creates a Job through the CLI and gives it one piece and dispatch
// with raw content, in the given dispatch status, straight in the database.
func (s *sandbox) seedJob(title, dispatchStatus string) string {
	s.t.Helper()
	var job struct{ ID string }
	if err := json.Unmarshal([]byte(s.run("job", "create", "--title", title)), &job); err != nil {
		s.t.Fatal(err)
	}
	s.sql("INSERT INTO pieces (job_id, piece, guild, status) VALUES (?, 'p', 'masonry', 'open')", job.ID)
	s.sql(`INSERT INTO dispatches (id, job_id, piece, guild, round, brief, rendered, status, created_at)
		VALUES (?, ?, 'p', 'masonry', 0, '{}', 'RAW BRIEF', ?, ?)`, "d-"+job.ID, job.ID, dispatchStatus, time.Now().UTC().Format(time.RFC3339Nano))
	return job.ID
}

func (s *sandbox) sql(q string, args ...any) {
	s.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.st, "archwright.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q, args...); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sandbox) rendered(job string) string {
	s.t.Helper()
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(s.st, "archwright.db")+"?_pragma=busy_timeout(5000)")
	defer db.Close()
	var r string
	if err := db.QueryRow("SELECT rendered FROM dispatches WHERE job_id = ?", job).Scan(&r); err != nil {
		s.t.Fatal(err)
	}
	return r
}

func TestPruneDryRunThenRealLeavesMarker(t *testing.T) {
	s := newSandbox(t)
	old := s.seedJob("old", "reported")
	s.sql("UPDATE jobs SET outcome = 'accepted', ended_at = ? WHERE id = ?", time.Now().Add(-40*24*time.Hour).UTC().Format(time.RFC3339Nano), old)
	open := s.seedJob("open", "reported")

	var rep struct {
		DryRun bool `json:"dry_run"`
		Pruned []struct {
			JobID string `json:"job_id"`
		}
	}
	if err := json.Unmarshal([]byte(s.run("prune", "--dry-run")), &rep); err != nil || !rep.DryRun || len(rep.Pruned) != 1 || rep.Pruned[0].JobID != old {
		t.Fatalf("%+v %v", rep, err)
	}
	if s.rendered(old) != "RAW BRIEF" {
		t.Fatal("dry run changed something")
	}
	if err := json.Unmarshal([]byte(s.run("prune")), &rep); err != nil || len(rep.Pruned) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if s.rendered(old) != "" || s.rendered(open) != "RAW BRIEF" {
		t.Fatal("prune must clear only the ended Job's raw content")
	}
	if out := s.run("job", "show", old); !strings.Contains(out, "purged_at") {
		t.Fatalf("no marker: %s", out)
	}
}

func TestPurgeAndDeleteNeedConfirmationAtATerminal(t *testing.T) {
	s := newSandbox(t)
	id := s.seedJob("j", "reported")
	for _, verb := range []string{"purge", "delete"} {
		if out, err := s.cmd("y\n", "job", verb, id); err == nil || !strings.Contains(out, "terminal") {
			t.Fatalf("%s without a terminal: %v\n%s", verb, err, out)
		}
	}
	if s.rendered(id) != "RAW BRIEF" || !strings.Contains(s.run("job", "list"), id) {
		t.Fatal("a refused command must change nothing")
	}
}

func TestUpgradeRefusesWithActiveRunsAndRestartsWithout(t *testing.T) {
	s := newSandbox(t)
	id := s.seedJob("j", "running")
	var before struct{ PID int }
	json.Unmarshal([]byte(s.run("works", "status")), &before)
	if out, err := s.cmd("y\n", "upgrade"); err == nil || !strings.Contains(out, "active") {
		t.Fatalf("upgrade with a run active: %v\n%s", err, out)
	}
	var still struct{ PID int }
	json.Unmarshal([]byte(s.run("works", "status")), &still)
	if still.PID != before.PID || before.PID == 0 {
		t.Fatalf("the Works was restarted despite the refusal: %d -> %d", before.PID, still.PID)
	}

	s.sql("UPDATE dispatches SET status = 'reported' WHERE job_id = ?", id)
	var up struct {
		Restarted bool
		PID       int
	}
	if err := json.Unmarshal([]byte(s.run("upgrade")), &up); err != nil || !up.Restarted || up.PID == before.PID {
		t.Fatalf("%+v %v", up, err)
	}
}
