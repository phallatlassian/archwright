package store

import (
	"errors"
	"os"
	"testing"
	"time"
)

// replaceWAL swaps the live WAL and shm for fresh files, as happened to the
// Works in #131: the open connection keeps writing to files no path names.
func replaceWAL(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Rename(path+suffix, path+suffix+".orphan"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+suffix, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWritesRefusedOnceTheWALIsReplaced(t *testing.T) {
	s, path := open(t)
	if _, err := s.CreateJob("before", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Failed():
		t.Fatal("failed before anything was replaced")
	default:
	}
	replaceWAL(t, path)

	// SQLite itself does not object: this write succeeds into a WAL no path
	// names, and a restart would not see it. That is what the guard prevents.
	if _, err := s.db.DB.Exec("UPDATE jobs SET title = 'lost'"); err != nil {
		t.Fatalf("the unguarded handle was expected to write silently: %v", err)
	}
	if _, err := s.CreateJob("after", ""); !errors.Is(err, ErrWALReplaced) {
		t.Fatalf("write after replacement: want ErrWALReplaced, got %v", err)
	}
	if _, err := s.db.Exec("UPDATE jobs SET title = 'x'"); !errors.Is(err, ErrWALReplaced) {
		t.Fatalf("Exec: want ErrWALReplaced, got %v", err)
	}
	if _, err := s.AddRunEvent("run-1", "note", "x", nil); !errors.Is(err, ErrWALReplaced) {
		t.Fatalf("AddRunEvent: want ErrWALReplaced, got %v", err)
	}
	if !errors.Is(s.Failure(), ErrWALReplaced) {
		t.Fatalf("Failure: %v", s.Failure())
	}
	select {
	case <-s.Failed():
	default:
		t.Fatal("Failed not closed")
	}
	// Reads still answer so the operator can see what the Works knew.
	if jobs, err := s.ListJobs(); err != nil || len(jobs) != 1 {
		t.Fatalf("read after failure: %v %v", jobs, err)
	}
}

func TestUnlinkedWALIsDetected(t *testing.T) {
	s, path := open(t)
	os.Remove(path + "-wal")
	if _, err := s.CreateJob("after", ""); !errors.Is(err, ErrWALReplaced) {
		t.Fatalf("want ErrWALReplaced, got %v", err)
	}
}

func TestReplacementIsDetectedWithoutAWrite(t *testing.T) {
	old := walWatchEvery
	walWatchEvery = 10 * time.Millisecond
	t.Cleanup(func() { walWatchEvery = old })
	s, path := open(t)
	replaceWAL(t, path)
	select {
	case <-s.Failed():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher never noticed the replaced WAL")
	}
}

func TestCheckpointEmptiesTheWAL(t *testing.T) {
	s, path := open(t)
	if _, err := s.CreateJob("kept", ""); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("expected a non-empty WAL before the checkpoint: %v %v", fi, err)
	}
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + "-wal"); err != nil || fi.Size() != 0 {
		t.Fatalf("WAL after checkpoint: %v %v", fi, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if jobs, _ := s2.ListJobs(); len(jobs) != 1 {
		t.Fatalf("jobs after reopen: %d", len(jobs))
	}
}

func TestCheckpointAndCloseLeaveAReplacedWALAlone(t *testing.T) {
	s, path := open(t)
	replaceWAL(t, path)
	if err := s.checkWAL(); !errors.Is(err, ErrWALReplaced) {
		t.Fatalf("checkWAL: %v", err)
	}
	if err := s.Checkpoint(); !errors.Is(err, ErrWALReplaced) {
		t.Fatalf("Checkpoint after failure: want ErrWALReplaced, got %v", err)
	}
	if err := s.Close(); !errors.Is(err, ErrWALReplaced) {
		t.Fatalf("Close after failure: want ErrWALReplaced, got %v", err)
	}
	if fi, err := os.Stat(path + "-wal"); err != nil || fi.Size() != 0 {
		t.Fatalf("the replacement WAL was touched: %v %v", fi, err)
	}
}
