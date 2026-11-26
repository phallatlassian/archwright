package store

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// A Works that reads its own database for doctor must not open and close the
// database, WAL or shm by path: on POSIX close() drops the process's SQLite
// locks, so a later read-write opener unlinks the WAL under the Works and
// everything committed after is lost (docs/research/works-sqlite-wal-replaced-while-running.md).
func TestLiveSnapshotLeavesTheWorksLocksAndWALAlone(t *testing.T) {
	s, path := open(t)
	const rows = 101
	for i := 0; i < rows/2; i++ {
		if _, err := s.AddSite(Site{Name: fmt.Sprintf("s%d", i), URL: fmt.Sprintf("https://example.com/s%d.git", i), ClonePath: fmt.Sprintf("/x%d", i), BaseBranch: "main", ConcurrencyCap: 1}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.Snapshot() // the doctor gate running inside the running Works
	if err != nil {
		t.Fatal(err)
	}
	if sites, err := snap.Sites(); err != nil || len(sites) != rows/2 {
		t.Fatalf("snapshot sees %d sites, %v", len(sites), err)
	}
	snap.Close()

	// A second read-write opener in another process (`archwright approval
	// set`) that only reads. Locks are per process, so it must be a child.
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperReadWriteOpener$")
	cmd.Env = append(os.Environ(), "ARCHWRIGHT_TEST_OPEN_DB="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second opener: %v\n%s", err, out)
	}
	var n int

	for i := rows / 2; i < rows; i++ { // the Works keeps writing
		if _, err := s.AddSite(Site{Name: fmt.Sprintf("s%d", i), URL: fmt.Sprintf("https://example.com/s%d.git", i), ClonePath: fmt.Sprintf("/x%d", i), BaseBranch: "main", ConcurrencyCap: 1}); err != nil {
			t.Fatal(err)
		}
	}

	// A restart: a fresh opener must see every committed row.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow("SELECT count(*) FROM sites").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != rows {
		t.Fatalf("a fresh opener sees %d of %d sites: the WAL was replaced under the running store", n, rows)
	}
}

// TestHelperReadWriteOpener is the second opener, run as a child process: it
// opens the database read-write, runs a SELECT and closes it, which
// checkpoints and unlinks the WAL if it believes it is the only connection.
func TestHelperReadWriteOpener(t *testing.T) {
	path := os.Getenv("ARCHWRIGHT_TEST_OPEN_DB")
	if path == "" {
		t.Skip("helper process")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sites").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
