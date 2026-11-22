package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// ErrWALReplaced means the database's write-ahead log or shared-memory file is
// no longer the file this Works opened. SQLite keeps writing to the files it
// holds open, so every later commit would land in a log no path names and be
// lost on restart (#131). The Works refuses further writes instead.
var ErrWALReplaced = errors.New("the database's WAL or shm file was replaced or removed while the Works was running; writes since then are not on disk")

// walWatchEvery is how often the Works looks for a replaced WAL between writes.
var walWatchEvery = 5 * time.Second

// walGuard remembers which files held the WAL and shm when the database was
// opened and notices when the paths stop naming them.
type walGuard struct {
	paths []string
	ids   map[string]fileID

	mu     sync.Mutex
	err    error
	failed chan struct{}
}

type fileID struct{ dev, ino uint64 }

func newWALGuard(dbPath string) *walGuard {
	g := &walGuard{
		paths:  []string{dbPath + "-wal", dbPath + "-shm"},
		ids:    map[string]fileID{},
		failed: make(chan struct{}),
	}
	g.baseline()
	return g
}

func statID(path string) (fileID, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileID{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, fmt.Errorf("%s: no inode on this platform", path)
	}
	return fileID{uint64(st.Dev), uint64(st.Ino)}, nil
}

// baseline records the files as they are now. SQLite creates both on the first
// read of a WAL-mode database; one that does not exist yet is adopted the
// first time it is seen.
func (g *walGuard) baseline() {
	for _, p := range g.paths {
		if id, err := statID(p); err == nil {
			g.ids[p] = id
		}
	}
}

// check compares the files on disk with the baseline and trips the guard on
// the first difference. It returns the guard's failure, if any.
func (g *walGuard) check() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	for _, p := range g.paths {
		id, err := statID(p)
		want, known := g.ids[p]
		switch {
		case err != nil && known:
			g.trip(fmt.Errorf("%w: %s: %v", ErrWALReplaced, p, err))
		case err != nil: // not created yet
		case !known:
			g.ids[p] = id
		case id != want:
			g.trip(fmt.Errorf("%w: %s is now a different file", ErrWALReplaced, p))
		}
		if g.err != nil {
			return g.err
		}
	}
	return nil
}

func (g *walGuard) trip(err error) {
	g.err = err
	close(g.failed)
}

func (g *walGuard) failure() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

// guardedDB is the handle the Store writes through. Begin and Exec refuse once
// the WAL has been replaced; reads still answer so the state the Works held
// can be inspected.
type guardedDB struct {
	*sql.DB
	g *walGuard
}

func (d guardedDB) Begin() (*sql.Tx, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	return d.DB.Begin()
}

func (d guardedDB) Exec(query string, args ...any) (sql.Result, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	return d.DB.Exec(query, args...)
}

// check is the gate for statements that write outside Begin and Exec, such as
// INSERT ... RETURNING through QueryRow. A handle with no guard (the migration
// at open, a snapshot copy) passes.
func (d guardedDB) check() error {
	if d.g == nil {
		return nil
	}
	return d.g.check()
}

// Failed is closed once the database's WAL or shm file has been replaced or
// removed under the open connection. Writes then fail with ErrWALReplaced.
func (s *Store) Failed() <-chan struct{} { return s.guard.failed }

// Failure is the reason Failed was closed, or nil.
func (s *Store) Failure() error { return s.guard.failure() }

// checkWAL runs the guard now.
func (s *Store) checkWAL() error { return s.guard.check() }

// watchWAL notices a replaced WAL between writes, so the Works fails promptly
// even when it is idle. It returns when stop is closed or the guard trips.
func (s *Store) watchWAL(stop <-chan struct{}) {
	t := time.NewTicker(walWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if s.guard.check() != nil {
				return
			}
		}
	}
}

// checkpoint folds the WAL into the database file and truncates it, so a
// stopped Works leaves one self-contained file. It reports a checkpoint that
// could not finish (a foreign reader holds the log) rather than hiding it.
func (s *Store) checkpoint() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var busy, logFrames, moved int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &moved); err != nil {
		return fmt.Errorf("checkpointing the WAL: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("checkpointing the WAL: another connection holds the database (%d of %d frames moved)", moved, logFrames)
	}
	return nil
}
