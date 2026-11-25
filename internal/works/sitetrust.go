package works

import (
	"net/http"

	"github.com/phallatlassian/archwright/internal/envsnap"
)

// EnvHeader carries the developer's shell environment, allow-listed, on every
// call the `archwright` CLI makes; the Works keeps the latest for the Sessions
// it spawns (docs/spec/tmux-runtime.md).
const EnvHeader = "X-Archwright-Env"

// MsgProbeNotHosted is what a Works that does not host Sessions says to a probe request.
const MsgProbeNotHosted = "this Works does not host Sessions in tmux, so there is nothing to probe"

// UseTrustProbe makes registering a Site start its trust probe.
func (r *Runs) UseTrustProbe(p *TrustProber) {
	r.mu.Lock()
	r.probes = p
	r.mu.Unlock()
}

// UseEnv makes the Works keep the shell environment each CLI call reports.
func (r *Runs) UseEnv(h *envsnap.Holder) {
	r.mu.Lock()
	r.env = h
	r.mu.Unlock()
}

// ProbeSite starts the Site's trust probe in the background. It reports
// whether it started one (not while the Site's probe is running) and whether
// this Works probes at all.
func (r *Runs) ProbeSite(site string) (started, hosted bool) {
	r.mu.Lock()
	p := r.probes
	r.mu.Unlock()
	if p == nil {
		return false, false
	}
	return p.Start(site), true
}

// remember keeps the environment a CLI call reported. What it sends is
// filtered again here; a call that sent none, or garbage, changes nothing.
func (r *Runs) remember(req *http.Request) {
	r.mu.Lock()
	h := r.env
	r.mu.Unlock()
	v := req.Header.Get(EnvHeader)
	if h == nil || v == "" {
		return
	}
	if snap, err := envsnap.Decode(v); err == nil {
		h.Set(snap)
	}
}

// withEnv records the environment of every authorised call that carries one.
func withEnv(runs *Runs, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runs.remember(r)
		next.ServeHTTP(w, r)
	})
}
