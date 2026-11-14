// Package store is the Works' embedded SQLite database (ADR 0002): WAL mode,
// forward-only migrations and an append-only events table.
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNewerSchema means the database was written by a newer binary.
var ErrNewerSchema = errors.New("database schema is newer than this binary")

type migration struct {
	version int
	sql     string
}

// loadMigrationsFn is a seam for tests that need a schema ahead of the shipped one.
var loadMigrationsFn = loadMigrations

func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		num, _, _ := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("migration %q: name must start with a number", e.Name())
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{v, string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

// Store wraps the database handle.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// SchemaVersion is the schema version this binary writes.
func SchemaVersion() int {
	ms, err := loadMigrationsFn()
	if err != nil || len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].version
}

// Open opens (creating if needed) the database at path and migrates it.
// An existing database is copied aside before any migration runs; a database
// whose schema is newer than this binary is refused untouched.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(path); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) version() (int, error) {
	var v int
	err := s.db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

func (s *Store) migrate(path string) error {
	ms, err := loadMigrationsFn()
	if err != nil {
		return err
	}
	current, err := s.version()
	if err != nil {
		return err
	}
	latest := 0
	if len(ms) > 0 {
		latest = ms[len(ms)-1].version
	}
	if current > latest {
		return fmt.Errorf("%w: database is v%d, binary supports v%d; rebuild archwright", ErrNewerSchema, current, latest)
	}
	if current == latest {
		return nil
	}
	if current > 0 {
		if err := s.backup(path, current); err != nil {
			return fmt.Errorf("backing up before migration: %w", err)
		}
	}
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		if err := s.apply(m); err != nil {
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
	}
	return nil
}

func (s *Store) apply(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(m.sql); err != nil {
		return err
	}
	if _, err := tx.Exec("PRAGMA user_version = " + strconv.Itoa(m.version)); err != nil {
		return err
	}
	return tx.Commit()
}

// backup writes a consistent copy next to the database, named for the
// schema version it holds.
func (s *Store) backup(path string, version int) error {
	dest := fmt.Sprintf("%s.v%d.%s.bak", path, version, s.now().UTC().Format("20060102T150405.000000000Z"))
	if _, err := s.db.Exec("VACUUM INTO ?", dest); err != nil {
		return err
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return err
	}
	return pruneBackups(path, KeptBackups)
}

// KeptBackups is how many pre-migration database copies are kept (ADR 0016, 0017).
const KeptBackups = 3

// pruneBackups deletes all but the keep most recent pre-migration copies of
// the database at path. Names end in a fixed-width UTC stamp, so the name
// order is the age order.
func pruneBackups(path string, keep int) error {
	baks, err := Backups(path)
	if err != nil {
		return err
	}
	for len(baks) > keep {
		if err := os.Remove(baks[0]); err != nil {
			return err
		}
		baks = baks[1:]
	}
	return nil
}

// Backups lists the pre-migration copies of the database at path, oldest first.
func Backups(path string) ([]string, error) {
	baks, err := filepath.Glob(path + ".v*.bak")
	if err != nil {
		return nil, err
	}
	stamp := func(p string) string { // "<db>.v<N>.<stamp>.bak" -> "<stamp>"
		rest := strings.TrimPrefix(p, path+".v")
		_, ts, _ := strings.Cut(rest, ".")
		return ts
	}
	sort.Slice(baks, func(i, j int) bool { return stamp(baks[i]) < stamp(baks[j]) })
	return baks, nil
}
