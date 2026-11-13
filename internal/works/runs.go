package works

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// RunEnv is what a started run receives through its environment (ADR 0018).
type RunEnv struct {
	Run   string `json:"run"`
	Role  string `json:"role"`
	Spool string `json:"spool"`
	Token string `json:"token"`
}

// Environ renders the run's environment variables.
func (e RunEnv) Environ() []string {
	return []string{spool.EnvRun + "=" + e.Run, spool.EnvSpool + "=" + e.Spool, spool.EnvToken + "=" + e.Token}
}

// Runs tracks the runs this Works started and serves their spools. It never
// reads a spool it did not create.
type Runs struct {
	st   *store.Store
	poll time.Duration

	mu   sync.Mutex
	runs map[string]*run
}

type run struct {
	id, role, token, dir string
	stop                 chan struct{}
	done                 chan struct{}

	mu    sync.Mutex
	ended bool
	// answered remembers each handled request id's response, so a replayed
	// id gets the same answer and is never executed twice.
	answered map[string]spool.Response
}

// NewRuns returns an empty registry over st.
func NewRuns(st *store.Store) *Runs {
	return &Runs{st: st, poll: 25 * time.Millisecond, runs: map[string]*run{}}
}

// Start registers a run with the given role and creates its spool directory
// (mode 0700) under root, which the runner's adapter chooses.
func (r *Runs) Start(role, root string) (RunEnv, error) {
	switch role {
	case RoleArchwright, RoleWarden, RoleWright:
	default:
		return RunEnv{}, fmt.Errorf("unknown role %q", role)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return RunEnv{}, err
	}
	token, err := newToken()
	if err != nil {
		return RunEnv{}, err
	}
	suffix, err := newToken()
	if err != nil {
		return RunEnv{}, err
	}
	id := "run-" + suffix[:10]
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return RunEnv{}, err
	}
	ru := &run{id: id, role: role, token: token, dir: dir, stop: make(chan struct{}), done: make(chan struct{}), answered: map[string]spool.Response{}}
	r.mu.Lock()
	r.runs[id] = ru
	r.mu.Unlock()
	go r.watch(ru)
	return RunEnv{Run: id, Role: role, Spool: dir, Token: token}, nil
}

// End marks a run finished and removes its spool. Later requests are never read.
func (r *Runs) End(id string) error {
	r.mu.Lock()
	ru := r.runs[id]
	delete(r.runs, id)
	r.mu.Unlock()
	if ru == nil {
		return fmt.Errorf("run %q: %w", id, store.ErrNotFound)
	}
	ru.finish()
	return nil
}

// Close ends every run.
func (r *Runs) Close() {
	r.mu.Lock()
	all := r.runs
	r.runs = map[string]*run{}
	r.mu.Unlock()
	for _, ru := range all {
		ru.finish()
	}
}

func (ru *run) finish() {
	ru.mu.Lock()
	if ru.ended {
		ru.mu.Unlock()
		return
	}
	ru.ended = true
	close(ru.stop)
	ru.mu.Unlock()
	<-ru.done
	os.RemoveAll(ru.dir)
}

// watch scans the run's spool until the run ends.
func (r *Runs) watch(ru *run) {
	defer close(ru.done)
	tick := time.NewTicker(r.poll)
	defer tick.Stop()
	for {
		r.scan(ru)
		select {
		case <-ru.stop:
			return
		case <-tick.C:
		}
	}
}

func (r *Runs) scan(ru *run) {
	entries, err := os.ReadDir(ru.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if id, ok := spool.RequestID(e.Name()); ok {
			r.handle(ru, id)
		}
	}
}

func reject(id, code, msg string) spool.Response {
	return spool.Response{ID: id, Error: &spool.Error{Code: code, Message: msg}}
}

// handle answers one request file and removes it. Every request is untrusted.
func (r *Runs) handle(ru *run, id string) {
	defer os.Remove(spool.RequestPath(ru.dir, id))
	if prev, ok := ru.answered[id]; ok { // idempotent by id
		spool.WriteResponse(ru.dir, prev)
		return
	}
	resp, cache := r.respond(ru, id)
	if _, err := spool.WriteResponse(ru.dir, resp); err != nil {
		return
	}
	if cache {
		ru.answered[id] = resp
	}
}

// respond decides the answer; cache says whether a replay of id must repeat it
// (authentication failures are not remembered, so a corrected retry can succeed).
func (r *Runs) respond(ru *run, id string) (resp spool.Response, cache bool) {
	req, err := spool.ReadRequest(ru.dir, id)
	if err != nil || req.ID != id {
		return reject(id, spool.CodeBadRequest, "request is not valid JSON for this id"), false
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(ru.token)) != 1 {
		return reject(id, spool.CodeUnauthorized, "token does not match this run"), false
	}
	t, ok := catalogue[req.Tool]
	if !ok {
		return reject(id, spool.CodeUnknownTool, fmt.Sprintf("no tool %q", req.Tool)), true
	}
	if !t.allows(ru.role) {
		return reject(id, spool.CodeForbidden, fmt.Sprintf("tool %q is not available to role %q", req.Tool, ru.role)), true
	}
	ru.mu.Lock()
	defer ru.mu.Unlock()
	if ru.ended {
		return reject(id, spool.CodeRunEnded, "run has ended"), true
	}
	out, err := t.call(r.st, req.Input)
	switch {
	case err == nil:
		b, merr := json.Marshal(out)
		if merr != nil {
			return reject(id, spool.CodeInvalidInput, merr.Error()), true
		}
		return spool.Response{ID: id, OK: true, Output: b}, true
	case errors.Is(err, store.ErrNotFound):
		return reject(id, spool.CodeNotFound, err.Error()), true
	default:
		return reject(id, spool.CodeInvalidInput, err.Error()), true
	}
}
