package works

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

// Defaults of the trust probe.
const (
	// DefaultTrustWait is how long a probe waits for the developer to answer a
	// trust dialog: they may not be at the terminal when a Site is registered.
	DefaultTrustWait = 15 * time.Minute
	// DefaultTrustAskAfter is how long a probe Session may take to start before
	// the developer is asked: a folder that is already trusted starts at once
	// and never needs the item.
	DefaultTrustAskAfter = 5 * time.Second

	trustProbePoll = 25 * time.Millisecond
	// trustStartSlack keeps the adapter's own start deadline behind the probe's.
	trustStartSlack = time.Minute
	trustEndWait    = 10 * time.Second
)

// trustProbeBrief is all a probe Session is told: it needs to start, not to work.
const trustProbeBrief = "This is a trust probe, not a task: the Archwright factory is checking that a Session can start in this folder. " +
	"Reply with the single word ready, run no command and edit no file."

// SiteWorktrees is what the probe needs of the Sites: the folder the Site's Job worktrees sit in.
type SiteWorktrees interface {
	ProbeDir(site string) (dir string, err error)
}

// TrustProber is the probe a Site's registration runs once (docs/spec/tmux-runtime.md):
// a Session of each installed runner, launched in the folder the Site's Job
// worktrees sit in. No hook fires before a CLI's
// folder-trust dialog is answered (and Codex's hook trust), so a Session that
// starts is the evidence both were given and that the hooks reach the Works.
// One that does not start raises a Needs-you item asking the developer to
// answer the dialog once. The Works never answers it, and never writes the
// trust configuration itself (ADR 0027). What each probe found is kept per
// Site and runner for `archwright doctor`.
type TrustProber struct {
	St      *store.Store
	Sites   SiteWorktrees
	Runners map[string]hosted.Runner // by runner name
	// Installed says whether a runner's CLI is on this machine; nil means all are.
	Installed func(runner string) bool
	// Wait is how long a probe waits for its Session to start, and AskAfter how
	// long before the developer is asked to accept trust; zero means the defaults.
	Wait, AskAfter time.Duration
	// Attach is the command that attaches a terminal to a window of the factory;
	// nil means tmux on the Works' own socket.
	Attach func(window string) string

	once    sync.Once
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	running map[string]bool
}

func (p *TrustProber) init() {
	p.once.Do(func() {
		p.ctx, p.cancel = context.WithCancel(context.Background())
		p.running = map[string]bool{}
	})
}

// Close stops the probes in flight: their Sessions are cancelled and their
// worktrees removed, and what they had not yet found stays unrecorded.
func (p *TrustProber) Close() {
	p.init()
	p.mu.Lock()
	p.cancel() // under mu: a claim that follows sees it, and none can add to wg after the wait below
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *TrustProber) wait() time.Duration {
	if p.Wait > 0 {
		return p.Wait
	}
	return DefaultTrustWait
}

func (p *TrustProber) askAfter() time.Duration {
	if p.AskAfter > 0 {
		return p.AskAfter
	}
	return DefaultTrustAskAfter
}

func (p *TrustProber) installed(runner string) bool { return p.Installed == nil || p.Installed(runner) }

func (p *TrustProber) attach(window string) string {
	if p.Attach != nil {
		return p.Attach(window)
	}
	return fmt.Sprintf("tmux -L %s attach-session -t =%s:%s", hosted.DefaultSocketName, hosted.DefaultSessionName, window)
}

// claim takes the Site's probe, so two never run in its folders at once.
func (p *TrustProber) claim(site string) bool {
	p.init()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running[site] || p.ctx.Err() != nil {
		return false
	}
	p.running[site] = true
	p.wg.Add(1)
	return true
}

func (p *TrustProber) release(site string) {
	p.mu.Lock()
	delete(p.running, site)
	p.mu.Unlock()
	p.wg.Done()
}

// Start probes the Site in the background and reports whether it did: a probe
// of that Site already running is not joined by a second.
func (p *TrustProber) Start(site string) bool {
	if !p.claim(site) {
		return false
	}
	go func() {
		defer p.release(site)
		if err := p.run(p.ctx, site); err != nil && p.ctx.Err() == nil {
			log.Printf("trust probe of Site %s: %v", site, err)
		}
	}()
	return true
}

// ErrProbeRunning is why a probe was not started: one is already running for the Site.
var ErrProbeRunning = errors.New("a trust probe of the Site is already running")

// Probe probes the Site and returns when every runner has been tried.
func (p *TrustProber) Probe(ctx context.Context, site string) error {
	if !p.claim(site) {
		return ErrProbeRunning
	}
	defer p.release(site)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	return p.run(ctx, site)
}

func (p *TrustProber) run(ctx context.Context, site string) error {
	if _, err := p.St.GetSite(site); err != nil {
		return err
	}
	names := make([]string, 0, len(p.Runners))
	for name := range p.Runners {
		names = append(names, name)
	}
	sort.Strings(names)
	var dir string
	for _, name := range names {
		if !p.installed(name) {
			p.record(site, name, store.TrustUnavailable, name+" is not installed (its CLI is not on PATH), so there is nothing to probe")
			p.St.ResolveNeedsYou(itemKey(site, name))
			continue
		}
		if dir == "" { // one folder for the Site's runners, made when the first needs it
			d, err := p.Sites.ProbeDir(site)
			if err != nil {
				for _, rest := range names {
					if p.installed(rest) {
						p.record(site, rest, store.TrustFailed, "cannot make the Site's probe folder: "+err.Error())
					}
				}
				return err
			}
			dir = d
		}
		p.probeOne(ctx, site, name, p.Runners[name], dir)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

func itemKey(site, runner string) string { return store.NeedsTrustProbe + ":" + site + ":" + runner }

func (p *TrustProber) record(site, runner, state, detail string) {
	if err := p.St.SetSiteTrust(site, runner, state, detail); err != nil {
		log.Printf("recording the trust probe of Site %s (%s): %v", site, runner, err)
	}
}

// ask files the item asking the developer to answer the dialog.
func (p *TrustProber) ask(site, runner, window string) {
	what := runner + "'s folder trust"
	if runner == "codex" {
		what += " and its hook trust (\"Trust all and continue\")"
	}
	attach := p.attach(window)
	_, err := p.St.AddNeedsYou(store.NewNeedsYou{
		Kind: store.NeedsTrustProbe, Site: site, DedupeKey: itemKey(site, runner),
		Summary: fmt.Sprintf("Accept %s for Site %s, once: attach to the probe Session in window %s and answer its dialog. "+
			"If the probe has already given up, run it again with `archwright site probe %s`.", what, site, window, site),
		Detail: map[string]string{"runner": runner, "window": window, "attach": attach, "rerun": "archwright site probe " + site},
	})
	if err != nil {
		log.Printf("filing the trust probe item of Site %s (%s): %v", site, runner, err)
	}
}

// probeOne launches one runner's probe Session, waits for it to start, and ends it.
func (p *TrustProber) probeOne(ctx context.Context, site, name string, rn hosted.Runner, dir string) {
	window := "probe-" + site
	wait := p.wait()
	p.record(site, name, store.TrustPending, "waiting for a Session to start")
	s, err := rn.Launch(ctx, hosted.LaunchRequest{
		Run: "probe-" + randomID(), Job: window, Role: "probe", Brief: trustProbeBrief, Dir: dir,
		StartTimeout: wait + trustStartSlack,
	})
	if err != nil {
		if ctx.Err() == nil {
			p.record(site, name, store.TrustFailed, "the probe Session could not be launched: "+err.Error())
		}
		return
	}
	var res hosted.Result
	ended := make(chan struct{})
	go func() { res = s.Result(); close(ended) }()
	defer func() {
		s.Cancel()
		select {
		case <-ended:
		case <-time.After(trustEndWait):
		}
	}()
	started := func() bool {
		st := s.State()
		return (st != hosted.StateStarting && st != hosted.StateEnded) || s.Info().SessionID != ""
	}
	begun := time.Now()
	tick := time.NewTicker(trustProbePoll)
	defer tick.Stop()
	asked := false
	for {
		switch {
		case started():
			p.record(site, name, store.TrustCaptured, "a Session started in a worktree of the Site: folder trust (and Codex hook trust) was given there, and the hooks reach the Works")
			p.St.ResolveNeedsYou(itemKey(site, name))
			return
		case ctx.Err() != nil:
			return
		case time.Since(begun) >= wait:
			p.record(site, name, store.TrustUnconfirmed, fmt.Sprintf("no SessionStart hook within %s: the trust dialog was not answered, or the hooks are blocked (managed policy) or not trusted", wait))
			p.ask(site, name, window)
			return
		case !asked && time.Since(begun) >= p.askAfter():
			asked = true
			p.ask(site, name, window)
		}
		select {
		case <-ended:
			if started() { // it started and was over before the next look
				continue
			}
			p.record(site, name, store.TrustFailed, "the probe Session ended before it started: "+res.Error)
			p.ask(site, name, window)
			return
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

func randomID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
