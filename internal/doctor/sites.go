package doctor

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// pocockDocs are the files the Pocock skills configuration points at
// (docs/agents/*), referenced from an "Agent skills" section of AGENTS.md.
var pocockDocs = []string{"docs/agents/issue-tracker.md", "docs/agents/triage-labels.md", "docs/agents/domain.md"}

// checkSites checks each registered Site's repository, skills configuration
// and gates, from the Works' own clone and the registry copy (snap), and the
// remote with a read-only `git ls-remote`.
func checkSites(ctx context.Context, r *collector, d Deps, snap *store.Snapshot) {
	if snap == nil {
		return // no readable current-schema database: nothing registered or already reported
	}
	sites, err := snap.Sites()
	if err != nil {
		r.fail("sites", "cannot list Sites: "+err.Error(), "re-run `archwright doctor`; if it persists restore the database copy next to "+d.Dirs.Database())
		return
	}
	if len(sites) == 0 {
		r.pass("sites", "no Sites registered; register one with `archwright site register --url URL --typecheck C --lint C --test C`")
		return
	}
	pending, _ := snap.GateChanges(store.ChangePending)
	for _, s := range sites {
		checkRepository(ctx, r, d, s)
		checkPocock(ctx, r, d, s)
		checkGates(r, d, s, pending)
	}
}

func checkRepository(ctx context.Context, r *collector, d Deps, s store.Site) {
	name := "site-repo:" + s.Name
	fix := fmt.Sprintf("re-register it: archwright site remove %s && archwright site register --url %s ...", s.Name, s.URL)
	if _, err := os.Stat(s.ClonePath); err != nil {
		r.fail(name, "the Works' clone is missing at "+s.ClonePath, fix)
		return
	}
	if out, err := d.Output(ctx, "git", "-C", s.ClonePath, "rev-parse", "--is-bare-repository"); err != nil || out != "true" {
		r.fail(name, "the Works' clone at "+s.ClonePath+" is not a valid repository", fix)
		return
	}
	ref := baseRef(s)
	if _, err := d.Output(ctx, "git", "-C", s.ClonePath, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		r.fail(name, fmt.Sprintf("the clone has no base branch %q (%s)", s.BaseBranch, ref),
			fmt.Sprintf("fetch it: git -C %s fetch origin +refs/heads/%s:%s ; if the branch was renamed, re-register the Site", s.ClonePath, s.BaseBranch, ref))
		return
	}
	if out, err := d.Output(ctx, "git", "ls-remote", "--exit-code", s.URL, "refs/heads/"+s.BaseBranch); err != nil {
		r.warn(name, fmt.Sprintf("the remote %s does not answer for %s: %s", s.URL, s.BaseBranch, firstLine(out)),
			fmt.Sprintf("check your network and credentials: git ls-remote %s (the Works fetches from it before each worktree)", s.URL))
		return
	}
	r.pass(name, "clone %s at %s; remote %s reachable", s.ClonePath, s.BaseBranch, s.URL)
}

func baseRef(s store.Site) string { return "refs/remotes/origin/" + s.BaseBranch }

// checkPocock looks for the Pocock skills configuration on the Site's base
// branch: an "Agent skills" section in AGENTS.md (or CLAUDE.md) and the docs
// it points at. Missing configuration is a warning: skills fall back to asking.
func checkPocock(ctx context.Context, r *collector, d Deps, s store.Site) {
	name := "site-skills:" + s.Name
	if _, err := os.Stat(s.ClonePath); err != nil {
		return // reported by the repository check
	}
	ref := baseRef(s)
	var section bool
	for _, f := range []string{"AGENTS.md", "CLAUDE.md"} {
		if out, err := d.Output(ctx, "git", "-C", s.ClonePath, "show", ref+":"+f); err == nil && strings.Contains(out, "## Agent skills") {
			section = true
			break
		}
	}
	var missing []string
	if !section {
		missing = append(missing, `an "## Agent skills" section in AGENTS.md`)
	}
	for _, doc := range pocockDocs {
		if _, err := d.Output(ctx, "git", "-C", s.ClonePath, "cat-file", "-e", ref+":"+doc); err != nil {
			missing = append(missing, doc)
		}
	}
	if len(missing) > 0 {
		r.warn(name, "the skills configuration on "+s.BaseBranch+" is incomplete; missing "+strings.Join(missing, ", "),
			fmt.Sprintf("in the Site's repository run the setup-matt-pocock-skills skill, commit docs/agents/ and the Agent skills section to %s and push; the Works fetches it", s.BaseBranch))
		return
	}
	r.pass(name, "Agent skills section and docs/agents/ present on %s", s.BaseBranch)
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)

// shellWords never name a program: gates run under `sh -c`.
var shellWords = map[string]bool{"cd": true, "export": true, "set": true, "exit": true, "true": true, "false": true, "test": true, "echo": true, "exec": true, "eval": true, "source": true}

func checkGates(r *collector, d Deps, s store.Site, pending []store.GateChange) {
	name := "site-gates:" + s.Name
	cmds := []struct{ gate, cmd string }{{store.GateTypecheck, s.Gates.Typecheck}, {store.GateLint, s.Gates.Lint}, {store.GateTest, s.Gates.Test}}
	configured := 0
	var missingTools []string
	for _, g := range cmds {
		if g.cmd == "" {
			continue
		}
		configured++
		if w := strings.Fields(g.cmd); len(w) > 0 && plainWord.MatchString(w[0]) && !shellWords[w[0]] {
			if _, err := d.LookPath(w[0]); err != nil {
				missingTools = append(missingTools, fmt.Sprintf("%s gate runs %q but %s is not on PATH", g.gate, g.cmd, w[0]))
			}
		}
	}
	for _, c := range pending {
		if c.Site == s.Name {
			r.warn(name, fmt.Sprintf("gate change %s is proposed and not approved; the old gates stay in force", c.ID),
				fmt.Sprintf("review it, then approve at a terminal: archwright site gate-approve %s (or archwright site gate-reject %s)", c.ID, c.ID))
		}
	}
	switch {
	case configured == 0:
		r.fail(name, "the Site has no gate commands, so a handback would be accepted unchecked",
			fmt.Sprintf(`propose them, then approve at a terminal: archwright site gates %s --typecheck "..." --lint "..." --test "..." ; archwright site gate-approve ID`, s.Name))
	case len(missingTools) > 0:
		r.warn(name, strings.Join(missingTools, "; "), "install the tool, or change the gate: archwright site gates "+s.Name+" --test \"...\" then approve it")
	default:
		r.pass(name, "%d gate command(s) configured", configured)
	}
}
