package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

// ---- tmux ----

func withOutput(d Deps, f func(name string, args []string) (string, bool)) Deps {
	inner := d.Output
	d.Output = func(ctx context.Context, name string, args ...string) (string, error) {
		if out, ok := f(filepath.Base(name), args); ok {
			if strings.HasPrefix(out, "ERR:") {
				return strings.TrimPrefix(out, "ERR:"), errors.New("exit status 1")
			}
			return out, nil
		}
		return inner(ctx, name, args...)
	}
	return d
}

func withTmux(d Deps, version string, serverErr string) Deps {
	d.LookPath = func(n string) (string, error) {
		if n == "tmux" {
			return "/bin/tmux", nil
		}
		return "/bin/" + n, nil
	}
	return withOutput(d, func(name string, args []string) (string, bool) {
		if name != "tmux" {
			return "", false
		}
		if len(args) == 1 && args[0] == "-V" {
			return version, true
		}
		if serverErr != "" {
			return "ERR:" + serverErr, true
		}
		return "", true
	})
}

func TestTmuxPassesWhenInstalledNewEnoughAndAbleToStartAServer(t *testing.T) {
	for _, v := range []string{"tmux 3.8", "tmux 3.3a", "tmux next-3.5", "tmux 3.0", "tmux 4.1"} {
		rep := Run(context.Background(), withTmux(newDeps(t), v, ""))
		if c := byName(rep, "tmux"); c.Status != Pass || !strings.Contains(c.Detail, v) {
			t.Errorf("%s: %+v", v, c)
		}
		if c := byName(rep, "tmux-server"); c.Status != Pass {
			t.Errorf("%s: %+v", v, c)
		}
	}
}

func TestMissingTmuxFailsWithHowToInstallItAndBlocksRuns(t *testing.T) {
	d := newDeps(t)
	look := d.LookPath
	d.LookPath = func(n string) (string, error) {
		if n == "tmux" {
			return "", errors.New("not found")
		}
		return look(n)
	}
	rep := Run(context.Background(), d)
	c := byName(rep, "tmux")
	if c.Status != Fail || !strings.Contains(c.Fix, "brew install tmux") || !strings.Contains(c.Fix, "apt") {
		t.Fatalf("tmux: %+v", c)
	}
	if rep.OK {
		t.Error("runs would start with no tmux, which every agent runs in (ADR 0026)")
	}
	if c := byName(rep, "tmux-server"); c.Status != "absent" {
		t.Errorf("no server can be started without tmux: %+v", c)
	}
}

func TestOldTmuxFailsWithAnUpgrade(t *testing.T) {
	rep := Run(context.Background(), withTmux(newDeps(t), "tmux 2.9a", ""))
	if c := byName(rep, "tmux"); c.Status != Fail || !strings.Contains(c.Detail, "2.9") || !strings.Contains(c.Fix, "upgrade") {
		t.Fatalf("%+v", c)
	}
}

func TestATmuxThatCannotStartAServerFailsWithWhatItSaid(t *testing.T) {
	rep := Run(context.Background(), withTmux(newDeps(t), "tmux 3.8", "error creating /tmp/x/t.sock (Operation not permitted)"))
	c := byName(rep, "tmux-server")
	if c.Status != Fail || !strings.Contains(c.Detail, "Operation not permitted") || c.Fix == "" {
		t.Fatalf("%+v", c)
	}
}

// ---- hook delivery ----

type fakeHooks HookResult

func (f fakeHooks) Probe(context.Context) HookResult { return HookResult(f) }

func TestHookDeliveryReportsTheSocketAndTheSpoolFallback(t *testing.T) {
	cases := []struct {
		name   string
		res    HookResult
		status Status
		in     string
	}{
		{"socket", HookResult{Socket: true, Spool: true}, Pass, "socket"},
		{"spool only", HookResult{Spool: true}, Warn, "spool"},
		{"nothing", HookResult{}, Warn, "never reached"},
		{"cannot run", HookResult{Err: "no such file"}, Warn, "no such file"},
	}
	for _, c := range cases {
		d := newDeps(t)
		d.Hooks = fakeHooks(c.res)
		got := byName(Run(context.Background(), d), "hooks")
		if got.Status != c.status || !strings.Contains(got.Detail, c.in) {
			t.Errorf("%s: %+v", c.name, got)
		}
		if c.status == Warn && !strings.Contains(got.Fix, "session-hook") {
			t.Errorf("%s: the fix does not say how to check the hook: %+v", c.name, got)
		}
	}
}

func TestTheRealHookCommandDeliversOverTheSocketAndTheSpool(t *testing.T) {
	exe := buildHookExe(t)
	res := HookDelivery{Exe: exe}.Probe(context.Background())
	if res.Err != "" || !res.Socket || !res.Spool {
		t.Fatalf("%+v", res)
	}
	if res := (HookDelivery{Exe: filepath.Join(t.TempDir(), "missing")}).Probe(context.Background()); res.Err == "" {
		t.Errorf("a hook command that cannot run was reported delivered: %+v", res)
	}
}

// ---- trust ----

func registeredSite(t *testing.T, d Deps, name string) {
	t.Helper()
	st, err := store.Open(d.Dirs.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddSite(store.Site{Name: name, URL: "https://github.com/o/" + name, ClonePath: filepath.Join(t.TempDir(), name+".git"), BaseBranch: "main", ConcurrencyCap: 3, Gates: store.Gates{Test: "go test"}}); err != nil {
		t.Fatal(err)
	}
}

func recordTrust(t *testing.T, d Deps, site, runner, state, detail string) {
	t.Helper()
	st, err := store.Open(d.Dirs.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSiteTrust(site, runner, state, detail); err != nil {
		t.Fatal(err)
	}
}

func TestTrustIsReportedPerSiteAndRunnerFromTheProbe(t *testing.T) {
	d := newDeps(t)
	registeredSite(t, d, "shop")
	recordTrust(t, d, "shop", "claude", store.TrustCaptured, "started")
	recordTrust(t, d, "shop", "codex", store.TrustUnconfirmed, "no SessionStart hook within 15m0s")
	rep := Run(context.Background(), d)
	if c := byName(rep, "trust:shop:claude"); c.Status != Pass {
		t.Errorf("claude: %+v", c)
	}
	c := byName(rep, "trust:shop:codex")
	if c.Status != Warn || !strings.Contains(c.Detail, "no SessionStart") || !strings.Contains(c.Fix, "archwright site probe shop") || !strings.Contains(c.Fix, "Trust all and continue") {
		t.Errorf("codex: %+v", c)
	}
	for _, f := range rep.Failures() {
		if strings.HasPrefix(f.Name, "trust") {
			t.Errorf("an unanswered dialog blocks every run: %+v", f)
		}
	}
}

func TestASiteNobodyProbedAndAPendingProbeAreWarnings(t *testing.T) {
	d := newDeps(t)
	registeredSite(t, d, "old")
	registeredSite(t, d, "new")
	recordTrust(t, d, "new", "claude", store.TrustPending, "waiting")
	recordTrust(t, d, "new", "codex", store.TrustFailed, "the probe Session could not be launched: boom")
	rep := Run(context.Background(), d)
	if c := byName(rep, "trust:old"); c.Status != Warn || !strings.Contains(c.Fix, "archwright site probe old") {
		t.Errorf("a Site no probe has run for: %+v", c)
	}
	if c := byName(rep, "trust:new:claude"); c.Status != Warn || !strings.Contains(c.Detail, "waiting") {
		t.Errorf("pending: %+v", c)
	}
	if c := byName(rep, "trust:new:codex"); c.Status != Warn || !strings.Contains(c.Detail, "boom") {
		t.Errorf("failed: %+v", c)
	}
}

func TestARunnerThatIsNotInstalledNeedsNoTrust(t *testing.T) {
	d := newDeps(t)
	registeredSite(t, d, "shop")
	recordTrust(t, d, "shop", "codex", store.TrustUnavailable, "codex is not installed")
	if c := byName(Run(context.Background(), d), "trust:shop:codex"); c.Status != Pass {
		t.Errorf("%+v", c)
	}
}

// ---- prompts ----

// policy is a machine whose policy files are the ones written here.
type policy struct {
	t     *testing.T
	d     Deps
	files map[string][]string // runner -> files, in the order they are read
}

func newPolicy(t *testing.T) *policy {
	p := &policy{t: t, d: newDeps(t), files: map[string][]string{}}
	p.d.PolicyFiles = func(runner string) []string { return p.files[runner] }
	return p
}

func (p *policy) write(runner, name, content string) string {
	p.t.Helper()
	path := filepath.Join(p.t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		p.t.Fatal(err)
	}
	p.files[runner] = append(p.files[runner], path)
	return path
}

func (p *policy) run() (Report, []string) {
	var ran []string
	p.d = withOutput(p.d, func(name string, args []string) (string, bool) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return "", false
	})
	return Run(context.Background(), p.d), ran
}

func TestPushAndGhPromptByDefaultOnBothRunners(t *testing.T) {
	p := newPolicy(t)
	rep, _ := p.run()
	for _, name := range []string{"prompt:git-push", "prompt:gh"} {
		c := byName(rep, name)
		if c.Status != Pass || !strings.Contains(c.Detail, "claude") || !strings.Contains(c.Detail, "codex") || !strings.Contains(c.Detail, "asks") {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

func TestAClaudeAllowRuleMeansPushRunsWithoutAPromptAndTheFixNamesTheRule(t *testing.T) {
	p := newPolicy(t)
	f := p.write("claude", "settings.json", `{"permissions":{"allow":["Bash(npm test:*)","Bash(git push:*)"]}}`)
	rep, _ := p.run()
	c := byName(rep, "prompt:git-push")
	if c.Status != Warn || !strings.Contains(c.Detail, "claude: runs without a prompt") || !strings.Contains(c.Detail, "Bash(git push:*)") {
		t.Fatalf("%+v", c)
	}
	for _, want := range []string{f, `"ask"`, "Bash(git push:*)", "ADR 0028"} {
		if !strings.Contains(c.Fix, want) {
			t.Errorf("the fix does not say %q: %s", want, c.Fix)
		}
	}
	if gh := byName(rep, "prompt:gh"); gh.Status != Pass {
		t.Errorf("an allowed push says nothing about gh: %+v", gh)
	}
	for _, f := range rep.Failures() {
		if strings.HasPrefix(f.Name, "prompt") {
			t.Errorf("a developer's own policy blocks runs: %+v", f)
		}
	}
}

func TestClaudeRuleShapesAndPrecedence(t *testing.T) {
	cases := []struct {
		name, settings string
		push, gh       string // "asks", "runs without a prompt", "is denied"
	}{
		{"bare Bash", `{"permissions":{"allow":["Bash"]}}`, "runs without a prompt", "runs without a prompt"},
		{"Bash(*)", `{"permissions":{"allow":["Bash(*)"]}}`, "runs without a prompt", "runs without a prompt"},
		{"all of git", `{"permissions":{"allow":["Bash(git:*)"]}}`, "runs without a prompt", "asks"},
		{"gh pr", `{"permissions":{"allow":["Bash(gh pr:*)"]}}`, "asks", "runs without a prompt"},
		{"space star", `{"permissions":{"allow":["Bash(git push *)"]}}`, "runs without a prompt", "asks"},
		{"only another branch", `{"permissions":{"allow":["Bash(git push origin main)"]}}`, "asks", "asks"},
		{"a longer command", `{"permissions":{"allow":["Bash(git pushd:*)"]}}`, "asks", "asks"},
		{"ask beats allow", `{"permissions":{"allow":["Bash(git:*)"],"ask":["Bash(git push:*)"]}}`, "asks", "asks"},
		{"deny beats ask and allow", `{"permissions":{"allow":["Bash(*)"],"ask":["Bash(git push:*)"],"deny":["Bash(git push:*)"]}}`, "is denied", "runs without a prompt"},
		{"deny gh", `{"permissions":{"deny":["Bash(gh:*)"]}}`, "asks", "is denied"},
		{"bypass mode", `{"permissions":{"defaultMode":"bypassPermissions"}}`, "runs without a prompt", "runs without a prompt"},
		{"dontAsk mode with nothing allowed", `{"permissions":{"defaultMode":"dontAsk"}}`, "is denied", "is denied"},
		{"not Bash rules", `{"permissions":{"allow":["Read(*)","WebFetch(domain:github.com)"]}}`, "asks", "asks"},
		{"no permissions at all", `{"theme":"dark"}`, "asks", "asks"},
	}
	for _, c := range cases {
		p := newPolicy(t)
		p.write("claude", "settings.json", c.settings)
		rep, _ := p.run()
		if got := byName(rep, "prompt:git-push").Detail; !strings.Contains(got, "claude: "+c.push) {
			t.Errorf("%s: git push: %s, want claude %s", c.name, got, c.push)
		}
		if got := byName(rep, "prompt:gh").Detail; !strings.Contains(got, "claude: "+c.gh) {
			t.Errorf("%s: gh: %s, want claude %s", c.name, got, c.gh)
		}
	}
}

func TestManagedPolicyAcrossFilesDecidesAndADenyHasAnAdminFix(t *testing.T) {
	p := newPolicy(t)
	p.write("claude", "user.json", `{"permissions":{"allow":["Bash(git push:*)"]}}`)
	managed := p.write("claude", "managed-settings.json", `{"permissions":{"deny":["Bash(git push:*)"]}}`)
	c := byName(func() Report { r, _ := p.run(); return r }(), "prompt:git-push")
	if c.Status != Warn || !strings.Contains(c.Detail, "claude: is denied") || !strings.Contains(c.Detail, managed) {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(c.Fix, "whoever manages") || !strings.Contains(c.Fix, "open-pr") {
		t.Errorf("a denied push needs the administrator, and says what stops working: %s", c.Fix)
	}
}

func TestCodexRulesAndApprovalPolicy(t *testing.T) {
	cases := []struct {
		name, rules, config string
		push, gh            string
	}{
		{"allow rule", `prefix_rule(pattern=["git", "push"], decision="allow")`, "", "runs without a prompt", "asks"},
		{"prompt rule", `prefix_rule(pattern=["git", "push"], decision="prompt")`, "", "asks", "asks"},
		{"forbidden rule", `prefix_rule(pattern=["gh"], decision="forbidden")`, "", "asks", "is denied"},
		{"decision first, alternatives", "prefix_rule(\n  decision = \"allow\",\n  pattern = [\"gh\", [\"pr\", \"issue\"]],\n)", "", "asks", "runs without a prompt"},
		{"forbidden beats allow", "prefix_rule(pattern=[\"git\"], decision=\"allow\")\nprefix_rule(pattern=[\"git\",\"push\"], decision=\"forbidden\")", "", "is denied", "asks"},
		{"a longer pattern does not match", `prefix_rule(pattern=["git", "push", "--force"], decision="allow")`, "", "asks", "asks"},
		{"never asks", "", "approval_policy = \"never\"\n", "runs without a prompt", "runs without a prompt"},
		{"on-request", "", "approval_policy = \"on-request\"\n", "asks", "asks"},
		{"match examples after the pattern", "prefix_rule(\n  pattern = [\"git\", \"push\"],\n  decision = \"allow\",\n  match = [[\"git\", \"push\", \"origin\", \"main\"]],\n  justification = \"pushes (really) are fine\",\n)", "", "runs without a prompt", "asks"},
		{"profile approval_policy is not the default", "", "approval_policy = \"on-request\"\n[profiles.ci]\napproval_policy = \"never\"\n", "asks", "asks"},
		{"comments are not rules", "# prefix_rule(pattern=[\"git\", \"push\"], decision=\"allow\")", "", "asks", "asks"},
	}
	for _, c := range cases {
		p := newPolicy(t)
		if c.rules != "" {
			p.write("codex", "default.rules", c.rules)
		}
		if c.config != "" {
			p.write("codex", "config.toml", c.config)
		}
		rep, _ := p.run()
		if got := byName(rep, "prompt:git-push").Detail; !strings.Contains(got, "codex: "+c.push) {
			t.Errorf("%s: git push: %s, want codex %s", c.name, got, c.push)
		}
		if got := byName(rep, "prompt:gh").Detail; !strings.Contains(got, "codex: "+c.gh) {
			t.Errorf("%s: gh: %s, want codex %s", c.name, got, c.gh)
		}
	}
}

func TestACodexAllowFixIsARuleTheDeveloperAdds(t *testing.T) {
	p := newPolicy(t)
	p.write("codex", "default.rules", `prefix_rule(pattern=["gh"], decision="allow")`)
	rep, _ := p.run()
	c := byName(rep, "prompt:gh")
	if c.Status != Warn || !strings.Contains(c.Fix, `prefix_rule(pattern=["gh"], decision="prompt")`) || !strings.Contains(c.Fix, "the factory never writes it") {
		t.Fatalf("%+v", c)
	}
}

func TestAPolicyFileThatCannotBeReadIsSaidSoAndTheRestStillDecides(t *testing.T) {
	p := newPolicy(t)
	bad := p.write("claude", "settings.json", `{not json`)
	p.files["claude"] = append(p.files["claude"], filepath.Join(t.TempDir(), "absent.json")) // a missing file is simply not there
	rep, _ := p.run()
	c := byName(rep, "prompt:policy")
	if c.Status != Warn || !strings.Contains(c.Detail, bad) || c.Fix == "" {
		t.Fatalf("%+v", c)
	}
	if got := byName(rep, "prompt:git-push"); got.Status != Pass {
		t.Errorf("%+v", got)
	}
}

func TestOnlyInstalledRunnersAreEvaluated(t *testing.T) {
	p := newPolicy(t)
	look := p.d.LookPath
	p.d.LookPath = func(n string) (string, error) {
		if n == "codex" {
			return "", errors.New("not found")
		}
		return look(n)
	}
	rep, _ := p.run()
	if got := byName(rep, "prompt:git-push").Detail; !strings.Contains(got, "claude") || strings.Contains(got, "codex") {
		t.Errorf("%s", got)
	}
}

// Doctor reads policy files; it runs nothing that pushes, and nothing that
// changes a remote: the only commands it starts are reads.
func TestPromptChecksRunNoPushAndNoGhCommand(t *testing.T) {
	p := newPolicy(t)
	p.write("claude", "settings.json", `{"permissions":{"allow":["Bash(git push:*)","Bash(gh:*)"]}}`)
	_, ran := p.run()
	for _, c := range ran {
		if strings.Contains(c, " push") || strings.HasPrefix(c, "gh pr") || strings.HasPrefix(c, "gh issue") || strings.HasPrefix(c, "gh api") {
			t.Errorf("doctor ran %q", c)
		}
	}
}

// buildHookExe builds the archwright binary, whose session-hook is the command
// the CLIs run for every hook.
func buildHookExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "archwright")
	if out, err := exec.Command("go", "build", "-tags", "nodoctorgate", "-o", exe, "../../cmd/archwright").CombinedOutput(); err != nil {
		t.Fatalf("building archwright: %v\n%s", err, out)
	}
	return exe
}
