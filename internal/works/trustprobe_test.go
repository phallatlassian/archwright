package works

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/envsnap"
	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/store"
)

// probeRig is a registered Site and the trust prober over scripted Sessions
// standing in for claude and codex.
type probeRig struct {
	t       *testing.T
	st      *store.Store
	sm      *sites.Manager
	root    string
	claude  *hosted.Fake
	codex   *hosted.Fake
	p       *TrustProber
	missing map[string]bool
}

func newProbeRig(t *testing.T) *probeRig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db", "archwright.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	url := fakeHost(t, map[string]string{"a.txt": "a"})
	root, _ := filepath.EvalSymlinks(t.TempDir())
	sm := sites.New(st, filepath.Join(root, "worktrees"), filepath.Join(root, "clones"))
	if _, err := sm.Register(sites.RegisterInput{Name: "demo", URL: url, BaseBranch: "main", Gates: &store.Gates{}}); err != nil {
		t.Fatal(err)
	}
	r := &probeRig{t: t, st: st, sm: sm, root: filepath.Join(root, "worktrees"), missing: map[string]bool{},
		claude: hosted.NewFake(hosted.Turn(nil)), codex: hosted.NewFakeFor("codex", hosted.Turn(nil))}
	r.p = &TrustProber{
		St: st, Sites: sm, Runners: map[string]hosted.Runner{"claude": r.claude, "codex": r.codex},
		Installed: func(name string) bool { return !r.missing[name] },
		Wait:      300 * time.Millisecond, AskAfter: 40 * time.Millisecond,
	}
	t.Cleanup(r.p.Close)
	return r
}

// atTheDialog is a Session that sits at a dialog for d before its first hook.
func atTheDialog(d time.Duration) hosted.Script {
	return hosted.Script{Steps: []hosted.Step{
		{Pause: d, Hook: &hosted.Hook{Event: hosted.HookSessionStart, SessionID: "s1"}},
		{Hook: &hosted.Hook{Event: hosted.HookUserPromptSubmit}},
		{Hook: &hosted.Hook{Event: hosted.HookStop}},
	}}
}

func (r *probeRig) trust(runner string) store.SiteTrust {
	r.t.Helper()
	all, err := r.st.SiteTrusts("demo")
	if err != nil {
		r.t.Fatal(err)
	}
	for _, tr := range all {
		if tr.Runner == runner {
			return tr
		}
	}
	return store.SiteTrust{Runner: runner, State: "(none)"}
}

func (r *probeRig) items() []store.NeedsYouItem {
	items, err := r.st.NeedsYou()
	if err != nil {
		r.t.Fatal(err)
	}
	return items
}

func (r *probeRig) noWorktreeLeft() {
	r.t.Helper()
	entries, _ := os.ReadDir(filepath.Join(r.root, "demo"))
	if len(entries) != 0 {
		r.t.Errorf("the probe left %d worktree(s) behind", len(entries))
	}
}

// mainCheckout is where the probe runs: the Site's main checkout.
func (r *probeRig) mainCheckout() string {
	r.t.Helper()
	s, err := r.st.GetSite("demo")
	if err != nil {
		r.t.Fatal(err)
	}
	return s.ClonePath
}

func TestProbeCapturesTrustWhenASessionStartsInTheSitesMainCheckout(t *testing.T) {
	r := newProbeRig(t)
	if err := r.p.Probe(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if got := r.trust(name); got.State != store.TrustCaptured {
			t.Errorf("%s: %+v", name, got)
		}
	}
	launches := r.claude.Launches()
	if len(launches) != 1 {
		t.Fatalf("claude was launched %d times", len(launches))
	}
	l := launches[0]
	if l.Dir != r.mainCheckout() || filepath.Base(l.Dir) != "main" || l.Job != "probe-demo" || l.Role != "probe" {
		t.Errorf("the probe Session ran as %+v, want the Site's main checkout, in the Site's probe window", l)
	}
	if !strings.Contains(l.Brief, "trust probe") || strings.Contains(strings.ToLower(l.Brief), "archwright tool") {
		t.Errorf("brief = %q: the probe asks for nothing but to start", l.Brief)
	}
	if l.StartTimeout <= r.p.Wait {
		t.Errorf("the Session gives up starting at %s, before the probe stops waiting (%s)", l.StartTimeout, r.p.Wait)
	}
	for _, s := range r.claude.Sessions() {
		if res := s.Result(); res.Status != hosted.StatusCancelled {
			t.Errorf("the probe Session was left open: %+v", res)
		}
	}
	if got := r.items(); len(got) != 0 {
		t.Errorf("a probe that found trust given left %d item(s) for the developer: %+v", len(got), got)
	}
	r.noWorktreeLeft()
}

func TestProbeAsksTheDeveloperToAcceptTrustOnceAndClearsTheItemWhenTheyDo(t *testing.T) {
	r := newProbeRig(t)
	r.claude.SetScript(atTheDialog(200 * time.Millisecond)) // the developer takes their time
	r.missing["codex"] = true
	done := make(chan error, 1)
	go func() { done <- r.p.Probe(t.Context(), "demo") }()
	eventually(t, "the Needs-you item", func() bool { return len(r.items()) == 1 })
	it := r.items()[0]
	if it.Kind != store.NeedsTrustProbe || it.Site != "demo" || it.JobID != "" {
		t.Errorf("item = %+v, want a trust-probe item of the Site, with no Job", it)
	}
	for _, want := range []string{"claude", "folder trust", "probe-demo", "tmux", "archwright site probe demo"} {
		if !strings.Contains(it.Summary+string(it.Detail), want) {
			t.Errorf("the item does not say %q: %s %s", want, it.Summary, it.Detail)
		}
	}
	if got := r.trust("claude"); got.State != store.TrustPending {
		t.Errorf("while waiting: %+v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := r.trust("claude"); got.State != store.TrustCaptured {
		t.Errorf("after the developer accepted: %+v", got)
	}
	if got := r.items(); len(got) != 0 {
		t.Errorf("the item outlived the dialog: %+v", got)
	}
}

func TestProbeSaysSoWhenNoSessionStartsAndKeepsTheItemUntilARerunCapturesTrust(t *testing.T) {
	r := newProbeRig(t)
	r.claude.SetScript(atTheDialog(time.Hour)) // never answered
	r.missing["codex"] = true
	if err := r.p.Probe(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	got := r.trust("claude")
	if got.State != store.TrustUnconfirmed || !strings.Contains(got.Detail, "SessionStart") {
		t.Fatalf("trust = %+v", got)
	}
	if len(r.items()) != 1 {
		t.Fatalf("items = %+v, want the one still asking", r.items())
	}
	if res := r.claude.Sessions()[0].Result(); res.Status != hosted.StatusCancelled {
		t.Errorf("the stuck probe Session was left in its pane: %+v", res)
	}
	r.noWorktreeLeft()

	r.claude.SetScript(hosted.Turn(nil)) // the developer has accepted it since
	if err := r.p.Probe(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	if got := r.trust("claude"); got.State != store.TrustCaptured || len(r.items()) != 0 {
		t.Errorf("after the rerun: %+v, items %+v", got, r.items())
	}
}

func TestProbeRecordsASessionThatDiedBeforeStartingAsFailed(t *testing.T) {
	r := newProbeRig(t)
	r.claude.SetScript(hosted.Script{NoStart: true})
	r.missing["codex"] = true
	if err := r.p.Probe(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	if got := r.trust("claude"); got.State != store.TrustFailed || !strings.Contains(got.Detail, "failed to start") {
		t.Errorf("trust = %+v", got)
	}
}

func TestProbeSkipsARunnerThatIsNotInstalledAndAsksOnlyForCodexHookTrustOnCodex(t *testing.T) {
	r := newProbeRig(t)
	r.missing["claude"] = true
	r.codex.SetScript(atTheDialog(150 * time.Millisecond))
	done := make(chan error, 1)
	go func() { done <- r.p.Probe(t.Context(), "demo") }()
	eventually(t, "the Needs-you item", func() bool { return len(r.items()) == 1 })
	if s := r.items()[0].Summary; !strings.Contains(s, "codex") || !strings.Contains(s, "hook") {
		t.Errorf("a Codex probe also asks for hook trust: %q", s)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := r.trust("claude"); got.State != store.TrustUnavailable {
		t.Errorf("claude: %+v", got)
	}
	if n := len(r.claude.Launches()); n != 0 {
		t.Errorf("claude is not installed and was launched %d times", n)
	}
	if got := r.trust("codex"); got.State != store.TrustCaptured {
		t.Errorf("codex: %+v", got)
	}
}

func TestStartRunsOneProbeAtATimePerSiteInTheBackground(t *testing.T) {
	r := newProbeRig(t)
	r.claude.SetScript(atTheDialog(200 * time.Millisecond))
	r.missing["codex"] = true
	if !r.p.Start("demo") {
		t.Fatal("the probe did not start")
	}
	if r.p.Start("demo") {
		t.Error("a second probe of the same Site started beside the first")
	}
	eventually(t, "trust captured", func() bool { return r.trust("claude").State == store.TrustCaptured })
	eventually(t, "the prober to be free again", func() bool { return r.p.Start("demo") })
}

func TestCloseCancelsAProbeInFlightAndLeavesNothingBehind(t *testing.T) {
	r := newProbeRig(t)
	r.claude.SetScript(atTheDialog(time.Hour))
	r.p.Wait = time.Hour
	r.missing["codex"] = true
	r.p.Start("demo")
	eventually(t, "the probe Session", func() bool { return len(r.claude.Sessions()) == 1 })
	r.p.Close()
	if res := r.claude.Sessions()[0].Result(); res.Status != hosted.StatusCancelled {
		t.Errorf("the Session was left running: %+v", res)
	}
	r.noWorktreeLeft()
}

// probeAPI is the HTTP API over the rig's Sites and prober.
func (r *probeRig) probeAPI(runs *Runs) *dash {
	r.t.Helper()
	srv := httptest.NewServer(newHandler(r.st, runs, r.sm, testToken, 1, func() {}, nil))
	r.t.Cleanup(srv.Close)
	return &dash{rig: &rig{t: r.t, st: r.st}, srv: srv}
}

func TestRegisteringASiteStartsItsTrustProbeAndTheProbeCanBeRunAgain(t *testing.T) {
	r := newProbeRig(t)
	r.missing["codex"] = true
	runs := NewRuns(r.st)
	t.Cleanup(runs.Close)
	runs.UseSites(r.sm)
	runs.UseTrustProbe(r.p)
	api := r.probeAPI(runs)

	// Registering a Site (here, again) starts its probe.
	site, _ := r.st.GetSite("demo")
	if _, err := r.sm.Remove("demo"); err != nil {
		t.Fatal(err)
	}
	if all, _ := r.st.SiteTrusts("demo"); len(all) != 0 {
		t.Fatalf("trust of a removed Site: %+v", all)
	}
	code, body := api.do("POST", "/sites", `{"name":"demo","url":"`+site.URL+`","base_branch":"main","gates":{}}`, nil)
	if code != 201 {
		t.Fatalf("POST /sites = %d %s", code, body)
	}
	eventually(t, "the new Site's probe to capture trust", func() bool { return r.trust("claude").State == store.TrustCaptured })
	eventually(t, "that probe to finish", func() bool { r.p.mu.Lock(); defer r.p.mu.Unlock(); return !r.p.running["demo"] })

	r.claude.SetScript(atTheDialog(150 * time.Millisecond))
	code, body = api.do("POST", "/sites/demo/probe", ``, nil)
	if code != 202 || !strings.Contains(body, `"started":true`) {
		t.Fatalf("POST /sites/demo/probe = %d %s", code, body)
	}
	if code, body = api.do("POST", "/sites/demo/probe", ``, nil); code != 202 || !strings.Contains(body, `"started":false`) {
		t.Fatalf("a probe already running: %d %s", code, body)
	}
	eventually(t, "the rerun to capture trust", func() bool { return r.trust("claude").State == store.TrustCaptured })
	if code, _ := api.do("POST", "/sites/nope/probe", ``, nil); code != 404 {
		t.Errorf("a Site that is not registered: %d", code)
	}

	bare := NewRuns(r.st)
	t.Cleanup(bare.Close)
	if code, body := r.probeAPI(bare).do("POST", "/sites/demo/probe", ``, nil); code != http.StatusNotImplemented || !strings.Contains(body, "tmux") {
		t.Errorf("a Works that hosts no Sessions: %d %s", code, body)
	}
}

func TestACLICallsRefreshesTheEnvironmentSnapshotSessionsAreSpawnedWith(t *testing.T) {
	r := newProbeRig(t)
	runs := NewRuns(r.st)
	t.Cleanup(runs.Close)
	holder := envsnap.NewHolder([]string{"PATH=/at/start"})
	runs.UseEnv(holder)
	api := r.probeAPI(runs)

	hdr := envsnap.Encode(map[string]string{"PATH": "/from/the/cli", "SECRET_TOKEN": "x", "ARCHWRIGHT_RUN": "r"})
	if code, _ := api.do("GET", "/needs-you", "", map[string]string{EnvHeader: hdr}); code != 200 {
		t.Fatalf("code %d", code)
	}
	if got := holder.Get(); got["PATH"] != "/from/the/cli" || len(got) != 1 {
		t.Errorf("snapshot = %v: the CLI's word is filtered again by the Works", got)
	}
	api.do("GET", "/needs-you", "", map[string]string{EnvHeader: "%%%not-base64"}) // a bad header changes nothing
	api.do("GET", "/needs-you", "", nil)
	if got := holder.Get(); got["PATH"] != "/from/the/cli" {
		t.Errorf("snapshot = %v", got)
	}
	// An unauthorised call, which the guard refuses, never reaches the snapshot.
	api.do("GET", "/needs-you", "", map[string]string{"Authorization": "Bearer wrong", EnvHeader: envsnap.Encode(map[string]string{"PATH": "/evil"})})
	if got := holder.Get(); got["PATH"] != "/from/the/cli" {
		t.Errorf("snapshot = %v", got)
	}
}

func TestTheSessionRunnerGivesItsRunnersTheEnvironmentSnapshot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db", "archwright.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t.Setenv("ARCHWRIGHT_STATE_HOME", t.TempDir())
	t.Setenv("ARCHWRIGHT_CONFIG_HOME", t.TempDir())
	rn, err := sessionRunnerFrom(os.Getenv, nil, st)
	if err != nil {
		t.Fatal(err)
	}
	sr := rn.(*SessionRunner)
	sr.UseBaseEnv(func() map[string]string { return map[string]string{"EDITOR": "vim"} })
	tm := sr.Runners["claude"].(*hosted.Tmux)
	if tm.BaseEnv == nil || tm.BaseEnv()["EDITOR"] != "vim" {
		t.Fatalf("the tmux adapter has no snapshot source")
	}
}

func TestCLIInstalledLooksForTheBinaryTheEnvironmentNames(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "my-claude")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	env := map[string]string{EnvClaudeBinary: bin, EnvCodexBinary: filepath.Join(dir, "nope")}
	inst := cliInstalled(func(k string) string { return env[k] })
	if !inst("claude") || inst("codex") {
		t.Errorf("claude %v, codex %v", inst("claude"), inst("codex"))
	}
}
