// Package works is the Works daemon: a single instance per user, serving the
// factory's API on loopback, started on demand by any command (ADR 0016).
package works

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/phallatlassian/archwright/internal/config"
	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/forge"
	"github.com/phallatlassian/archwright/internal/launch"
	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/store"
)

// ErrAlreadyRunning means another Works holds the single-instance lock.
var ErrAlreadyRunning = errors.New("works is already running")

// Info is the pid file's content: how to reach the running Works.
type Info struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// Addr is the loopback address of the Works.
func (i Info) Addr() string { return fmt.Sprintf("127.0.0.1:%d", i.Port) }

// ReadInfo reads the pid file.
func ReadInfo(d paths.Dirs) (Info, error) {
	var info Info
	b, err := os.ReadFile(d.PidFile())
	if err != nil {
		return info, err
	}
	return info, json.Unmarshal(b, &info)
}

func writeInfo(d paths.Dirs, info Info) error {
	b, _ := json.Marshal(info)
	tmp := d.PidFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.PidFile())
}

// Serve runs the Works until ctx is cancelled or a client posts /shutdown.
// It returns ErrAlreadyRunning, touching nothing, if another instance holds the lock.
// ready, if non-nil, is closed once the pid file is written.
func Serve(ctx context.Context, d paths.Dirs, ready chan<- struct{}) error {
	return ServeWith(ctx, d, ready, Options{})
}

// Options are what a caller adds to the Works.
type Options struct {
	// Preflight must pass before any run starts (ADR 0014); nil means none.
	Preflight func(context.Context) error
}

// ServeWith is Serve with options.
func ServeWith(ctx context.Context, d paths.Dirs, ready chan<- struct{}, opts Options) error {
	if err := d.Ensure(); err != nil {
		return err
	}
	lock, err := os.OpenFile(d.Lock(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return ErrAlreadyRunning
		}
		return err
	}

	st, err := store.Open(d.Database())
	if err != nil {
		return err
	}
	defer func() {
		// A clean stop leaves one self-contained database file; say so when
		// it could not. A replaced WAL was already logged as FATAL.
		if err := st.Checkpoint(); err != nil && !errors.Is(err, store.ErrWALReplaced) {
			log.Printf("checkpointing the database: %v", err)
		}
		if err := st.Close(); err != nil && !errors.Is(err, store.ErrWALReplaced) {
			log.Printf("closing the database: %v", err)
		}
	}()
	if err := st.InterruptReviews(); err != nil {
		return err
	}
	if err := st.InterruptGateRuns(); err != nil {
		return err
	}
	// In-flight Wrights are re-dispatched from their stored briefs; Resume queues them.
	if _, err := st.InterruptDispatches(); err != nil {
		return err
	}
	sm := sites.New(st, d.Worktrees(), d.Clones())
	runs := NewRuns(st)
	defer runs.Close()
	runs.UseSites(sm)
	runs.UseForge(forge.Default())
	runs.SetPreflight(opts.Preflight)
	reg := defs.New(defs.Shipped(), d.Config)
	ReloadDefinitions(reg)
	runs.UseDefinitions(reg)
	guilds := GuildsFromRegistry(reg, DefaultProfiles(), skills.Vendored())
	wrights := WrightRunnerFrom(os.Getenv, guilds, st)
	runs.Configure(guilds, wrights)
	warden, reviewer := WardenRunnersFrom(os.Getenv, guilds, st, wrights)
	if sr, ok := wrights.(*SessionRunner); ok { // Sessions in tmux: the Works owns the Archwright too
		runs.UseSessions(sr, 0)
		runs.UseArchwright(&ArchwrightHost{
			Runners:   sr.Runners,
			Choice:    func() (string, error) { return config.Runner(d) },
			Home:      launch.Home(d),
			SpoolRoot: func(runner string) string { return launch.SpoolRoot(runner, os.Getuid(), os.TempDir()) },
		})
	}
	runs.ConfigureWarden(warden)
	if reviewer != nil {
		runs.ConfigureReview(reviewer)
	}
	runs.UseSkills(SkillsDelivery{Catalogue: skills.Vendored(), RunDir: d.RunDir})
	runs.SetLimits(LimitsFromEnv(os.Getenv))
	runs.SetRetention(func() (time.Duration, error) { return config.Retention(d) })
	if rec, err := runs.RecoverSessions(); err != nil {
		log.Printf("recovering Sessions: %v", err)
	} else if rec.Adopted+rec.Resumable+rec.Lost+len(rec.Orphans) > 0 {
		log.Printf("Sessions of the last Works: %d adopted, %d to resume, %d lost, %d orphaned", rec.Adopted, rec.Resumable, rec.Lost, len(rec.Orphans))
	}
	if err := runs.Resume(); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	token, err := newToken()
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	// A WAL replaced under the open database loses every later commit (#131):
	// say so in the log and stop, rather than keep accepting work it cannot keep.
	go func() {
		select {
		case <-st.Failed():
			log.Printf("FATAL: %v; stopping the Works. Restart it; state committed since the replacement is lost. Do not open archwright.db while the Works runs.", st.Failure())
			stop()
		case <-ctx.Done():
		}
	}()
	// Prune at start and daily until the Works stops (ADR 0017); the database
	// stays open until the loop has returned.
	pruned := make(chan struct{})
	go func() { defer close(pruned); runs.pruneLoop(ctx) }()
	defer func() { stop(); <-pruned }()
	// Tee committed events to events.log; also stopped before the store closes.
	teed := make(chan struct{})
	if from, err := st.LastEventID(); err != nil {
		log.Printf("events.log: %v", err)
		close(teed)
	} else {
		go func() { defer close(teed); teeEvents(ctx, st, from, d.EventLog(), eventLogMax, eventLogEvery) }()
	}
	defer func() { stop(); <-teed }()
	done := make(chan struct{})
	srv := &http.Server{Handler: newHandler(st, runs, sm, token, os.Getpid(), stop, done)}
	srv.RegisterOnShutdown(func() { close(done) }) // ends the event streams, which Shutdown would otherwise wait on
	info := Info{PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, Token: token}
	if err := writeInfo(d, info); err != nil {
		return err
	}
	// release drops the pid file and flock. It runs before the listener closes
	// so a stopped Works is never observed as down while still holding the lock.
	var once sync.Once
	release := func() {
		once.Do(func() {
			lock.Close() // releases the flock
			// A successor may already have replaced the pid file.
			if cur, err := ReadInfo(d); err == nil && cur.PID == info.PID {
				os.Remove(d.PidFile())
			}
		})
	}
	defer release()
	if ready != nil {
		close(ready)
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	release()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	if failure := st.Failure(); failure != nil {
		return failure
	}
	return err
}

// LogFile opens the daemon log for appending.
func LogFile(d paths.Dirs) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(d.Log()), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(d.Log(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
