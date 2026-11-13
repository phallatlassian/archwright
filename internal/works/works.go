// Package works is the Works daemon: a single instance per user, serving the
// factory's API on loopback, started on demand by any command (ADR 0016).
package works

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/sites"
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
	defer st.Close()
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
	// Until the vendor adapters land, dispatched Wrights run on the fake runner.
	reg := defs.New(defs.Shipped(), d.Config)
	ReloadDefinitions(reg)
	runs.UseDefinitions(reg)
	fake := NewFakeRunner(nil)
	if d, err := time.ParseDuration(os.Getenv("ARCHWRIGHT_FAKE_RUN_DELAY")); err == nil {
		fake.WithDelay(d) // lets a live check kill the Works while a Wright is in flight
	}
	runs.Configure(GuildsFromRegistry(reg, DefaultProfiles()), fake)
	runs.SetLimits(LimitsFromEnv(os.Getenv))
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
	return srv.Shutdown(shutdownCtx)
}

// LogFile opens the daemon log for appending.
func LogFile(d paths.Dirs) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(d.Log()), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(d.Log(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
