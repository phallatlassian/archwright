package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Snapshot is a private, read-only view of the database taken without
// touching it: the database file and its write-ahead log are copied to a
// temporary directory and opened there, never migrated. `archwright doctor`
// reads the registry this way because opening the real file would create WAL
// files, take locks or migrate it, and doctor changes nothing.
type Snapshot struct {
	// Version is the schema version of the copy (the database's user_version).
	Version int
	st      *Store
	dir     string
}

// OpenSnapshot copies the database at path and opens the copy. It returns an
// error wrapping os.ErrNotExist when there is no database yet.
func OpenSnapshot(path string) (*Snapshot, error) {
	dir, err := os.MkdirTemp("", "archwright-snapshot-")
	if err != nil {
		return nil, err
	}
	copyPath := filepath.Join(dir, "snapshot.db")
	// The log is copied before the database so a checkpoint between the two
	// copies can only make the copy newer than the log, never older.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := copyFile(path+suffix, copyPath+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	if err := copyFile(path, copyPath); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+copyPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	v, err := s.version()
	if err != nil {
		db.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &Snapshot{Version: v, st: s, dir: dir}, nil
}

// Sites lists the registered Sites. The snapshot must be at the current
// schema version, or the columns may differ.
func (s *Snapshot) Sites() ([]Site, error) { return s.st.ListSites() }

// GateChanges lists proposed gate changes with the given status.
func (s *Snapshot) GateChanges(status string) ([]GateChange, error) {
	return s.st.GateChanges(status)
}

// Close deletes the copy.
func (s *Snapshot) Close() error {
	err := s.st.Close()
	os.RemoveAll(s.dir)
	return err
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
