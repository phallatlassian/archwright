package works

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/envsnap"
	"github.com/phallatlassian/archwright/internal/forge"
	"github.com/phallatlassian/archwright/internal/launch"
	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// ErrWorksStopping is the cause of the context every run holds when the Works
// stops, as against a developer cancelling a run. A runner that hosts a
// Session leaves it open in its pane on this cause and returns an error
// wrapping it, so the next Works adopts the Session (ADR 0029).
var ErrWorksStopping = errors.New("the Works is stopping")

// SessionDetacher is a set of Sessions that can be told the Works has stopped.
type SessionDetacher interface{ Detach() }

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

	// Dispatch (ADR 0004, 0006): the Guilds that standards and limits come
	// from, the Runner Wrights run on, and where their spools live.
	guilds    GuildSource
	defs      *defs.Registry // the loaded Guild and Jig catalogue (ADR 0009)
	runner    Runner
	reviewer  ReviewRunner // runs Reviews (ADR 0012)
	warden    WardenRunner // runs the Wardens of started Plan steps (ADR 0020)
	skills    *SkillsDelivery
	forge     forge.Forge       // what Forge tools act through (ADR 0022); nil means forge.Default
	spoolRoot string            // when set, every run's spool lives here; otherwise each run's runner picks (ADR 0018)
	wrightWts map[string]string // dispatch id -> its Wright's worktree, kept across a rate-limit requeue
	bg        context.Context
	cancelBg  context.CancelCauseFunc
	wg        sync.WaitGroup
	nudges    sync.WaitGroup // posts of the inbox nudge still in flight

	// Gates (ADR 0010): where a handback branch is checked out, and each command's limit.
	sites       *sites.Manager
	gateTimeout time.Duration

	sched *scheduler // when dispatched Wrights may start (ADR 0008)

	// The Archwright the Works hosts as a Session, when it does (archwrightsession.go).
	archMu    sync.Mutex // serialises spawning it, launch included
	archHost  *ArchwrightHost
	archCurMu sync.Mutex // guards arch alone, so reading it never waits on a launch
	arch      *archSession

	// probes runs the trust probe of each registered Site, and env is the
	// developer's shell environment as the CLI last reported it (trustprobe.go, envsnap).
	probes *TrustProber
	env    *envsnap.Holder

	// sessions are the open Sessions kept until their Job leaves active work (ADR 0029).
	sessions         JobSessions
	sessionRetention time.Duration

	// preflight, when set, must pass before any run starts (ADR 0014): it is
	// `archwright doctor`. A failure refuses the run with its fixes.
	preflight func(context.Context) error

	retention func() (time.Duration, error) // how long raw run content is kept (ADR 0017)

	mu   sync.Mutex
	runs map[string]*run
	// cancels stops the run of a started dispatch; cancelled marks the
	// dispatches the developer cancelled, so their end is not a failure to report.
	cancels   map[string]context.CancelFunc
	cancelled map[string]bool
}

type run struct {
	id, role, token, dir string
	ctx                  RunContext
	stop                 chan struct{}
	ending               chan struct{} // closed when finish begins, without ru.mu, so a blocked tool call can give up
	endingOnce           sync.Once
	done                 chan struct{}

	// sess is where the session of an Archwright run listens (nudge.go);
	// markMu serialises the unread marker in the run's spool.
	sessMu sync.Mutex
	sess   *session
	markMu sync.Mutex

	mu    sync.Mutex
	ended bool
	// answered remembers each handled request id's response, so a replayed
	// id gets the same answer and is never executed twice.
	answered map[string]spool.Response
}

// NewRuns returns an empty registry over st.
func NewRuns(st *store.Store) *Runs {
	bg, cancel := context.WithCancelCause(context.Background())
	r := &Runs{st: st, poll: 25 * time.Millisecond, runs: map[string]*run{}, cancels: map[string]context.CancelFunc{}, cancelled: map[string]bool{}, wrightWts: map[string]string{}, bg: bg, cancelBg: cancel}
	r.sched = newScheduler(r)
	st.SetInboxNotifier(r.inboxChanged)
	r.wg.Add(1)
	go r.reapLoop()
	return r
}

// RunContext binds a run to the work it serves.
type RunContext struct {
	Job        string // the Job a Warden or Wright works for
	Guild      string // the Guild a Warden leads, or the Wright's Warden's
	DispatchID string // the dispatch a Wright carries out
	Review     string // the review a fresh Warden carries out
	Task       string // the Plan step a Warden carries out (ADR 0020)
}

// Configure sets where Guild definitions come from and which Runner carries
// out dispatched Wrights. Without them dispatch_wright fails closed.
func (r *Runs) Configure(guilds GuildSource, runner Runner) {
	r.mu.Lock()
	r.guilds, r.runner = guilds, runner
	r.mu.Unlock()
}

// Start registers a run with the given role and creates its spool directory
// (mode 0700) under root, which the runner's adapter chooses.
func (r *Runs) Start(role, root string) (RunEnv, error) {
	return r.StartFor(role, root, RunContext{})
}

// SetSpoolRoot puts every run's spool under root instead of the one its
// runner's sandbox can write (ADR 0018). It is for tests.
func (r *Runs) SetSpoolRoot(root string) {
	r.mu.Lock()
	r.spoolRoot = root
	r.mu.Unlock()
}

// spoolRootFor is where the spool of a run led by the named Guild goes: the
// root its bound runner's sandbox can write (ADR 0018). A Guild that cannot be
// looked up fails the run rather than falling back to a root the runner's
// sandbox may not be able to write (ADR 0011). With no Guilds configured, or a
// Guild with no runner named, the default per-user temp root is used.
func (r *Runs) spoolRootFor(guild string) (string, error) {
	r.mu.Lock()
	override, guilds := r.spoolRoot, r.guilds
	r.mu.Unlock()
	if override != "" {
		return override, nil
	}
	runnerName := ""
	if guilds != nil {
		g, err := guilds(guild)
		if err != nil {
			return "", fmt.Errorf("the spool root for guild %q: %w", guild, err)
		}
		runnerName = g.Binding.Runner
	}
	return launch.SpoolRoot(runnerName, os.Getuid(), os.TempDir()), nil
}

// UseForge sets the Forge the Forge tools act through. Without one the Works
// uses forge.Default.
func (r *Runs) UseForge(f forge.Forge) {
	r.mu.Lock()
	r.forge = f
	r.mu.Unlock()
}

func (r *Runs) forgeOrDefault() forge.Forge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.forge == nil {
		return forge.Default()
	}
	return r.forge
}

// SetPreflight makes every run start only while check passes.
func (r *Runs) SetPreflight(check func(context.Context) error) {
	r.mu.Lock()
	r.preflight = check
	r.mu.Unlock()
}

// StartFor is Start for a run bound to a Job, Guild or dispatch.
func (r *Runs) StartFor(role, root string, rc RunContext) (RunEnv, error) {
	r.mu.Lock()
	preflight := r.preflight
	r.mu.Unlock()
	if preflight != nil {
		if err := preflight(r.bg); err != nil {
			return RunEnv{}, err
		}
	}
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
	ru := &run{id: id, role: role, ctx: rc, token: token, dir: dir, stop: make(chan struct{}), ending: make(chan struct{}), done: make(chan struct{}), answered: map[string]spool.Response{}}
	r.mu.Lock()
	r.runs[id] = ru
	r.mu.Unlock()
	go r.watch(ru)
	r.syncUnread(ru) // items filed before the run started are unread
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
	r.cancelBg(ErrWorksStopping)
	probes := r.probes
	sessions := r.sessions
	r.mu.Unlock()
	if probes != nil {
		probes.Close() // its Sessions are cancelled and its worktrees removed
	}
	r.closeArchwright()
	r.sched.stop()
	r.wg.Wait() // dispatched Wrights end their own runs
	r.nudges.Wait()
	if d, ok := sessions.(SessionDetacher); ok {
		d.Detach() // the Sessions left in their panes are no longer followed or recorded
	}
	r.mu.Lock()
	all := r.runs
	r.runs = map[string]*run{}
	r.mu.Unlock()
	for _, ru := range all {
		ru.finish()
	}
}

func (ru *run) finish() {
	ru.endingOnce.Do(func() { close(ru.ending) })
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
	if prev, ok := ru.answered[id]; ok { // idempotent by id
		return prev, true
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
	out, err := t.call(&callCtx{st: r.st, runs: r, run: ru}, req.Input)
	var ce *codedError
	switch {
	case errors.As(err, &ce):
		return reject(id, ce.code, ce.msg), true
	case err == nil:
		b, merr := json.Marshal(out)
		if merr != nil {
			return reject(id, spool.CodeInvalidInput, merr.Error()), true
		}
		return spool.Response{ID: id, OK: true, Output: b}, true
	case errors.Is(err, store.ErrNotFound):
		return reject(id, spool.CodeNotFound, err.Error()), true
	case errors.Is(err, store.ErrConflict):
		return reject(id, spool.CodeConflict, err.Error()), true
	default:
		return reject(id, spool.CodeInvalidInput, err.Error()), true
	}
}
