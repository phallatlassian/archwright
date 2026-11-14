package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/phallatlassian/archwright/internal/launch"
)

type runnerSpec struct {
	name      string
	install   string
	ephemeral string   // the flag that keeps a run from persisting a session
	helpArgs  []string // where that flag is listed
	allowFix  string   // how the developer lets `archwright tool` run unprompted
}

var runnerSpecs = []runnerSpec{
	{
		name: launch.RunnerClaude, install: "install Claude Code so `claude` is on PATH (https://claude.com/claude-code)",
		ephemeral: "--no-session-persistence", helpArgs: []string{"--help"},
		allowFix: "add this rule once, in your own Claude Code settings (~/.claude/settings.json); the factory never writes it:\n" +
			`  {"permissions": {"allow": ["Bash(archwright tool:*)"]}}`,
	},
	{
		name: launch.RunnerCodex, install: "install the Codex CLI so `codex` is on PATH (npm install -g @openai/codex)",
		ephemeral: "--ephemeral", helpArgs: []string{"exec", "--help"},
		allowFix: "add this rule once, in your own Codex rules (~/.codex/rules/default.rules); the factory never writes it:\n" +
			`  prefix_rule(pattern=["archwright", "tool"], decision="allow")`,
	},
}

// checkRunner checks one runner: presence and version, ephemeral support,
// managed-policy notices, the unprompted `archwright tool` call and the tool
// transport from inside its sandbox. A runner the install does not use is
// only a warning when absent; an installed one must pass.
func checkRunner(ctx context.Context, r *collector, d Deps, spec runnerSpec, chosen string) {
	name := spec.name
	bin, err := d.LookPath(name)
	if err != nil {
		if name == chosen {
			r.fail("runner:"+name, name+" is the runner chosen for this install but is not on PATH", spec.install)
		} else {
			r.warn("runner:"+name, name+" is not installed; Guilds that use it cannot run", spec.install+" (or ignore this if you only use "+chosen+")")
		}
		return
	}
	version, err := d.Output(ctx, bin, "--version")
	if err != nil {
		r.fail("runner:"+name, fmt.Sprintf("`%s --version` failed: %v", bin, err), fmt.Sprintf("run `%s --version` yourself and repair the install", bin))
		return
	}
	r.pass("runner:"+name, "%s (%s)", firstLine(version), bin)

	help, err := d.Output(ctx, bin, spec.helpArgs...)
	if err != nil || !strings.Contains(help, spec.ephemeral) {
		r.fail("ephemeral:"+name, fmt.Sprintf("%s does not list %s, so runs would persist sessions", name, spec.ephemeral), "upgrade "+name+" to a version that has "+spec.ephemeral)
	} else {
		r.pass("ephemeral:"+name, "%s supports %s", name, spec.ephemeral)
	}

	res := d.Probe.Probe(ctx, name, bin)
	checkProbe(r, d, spec, res)
}

func checkProbe(r *collector, d Deps, spec runnerSpec, res ProbeResult) {
	name := spec.name
	root := launch.SpoolRoot(name, d.UID, d.TempDir)
	if res.Err != "" {
		r.fail("tool:"+name, "the probe run did not complete: "+res.Err,
			fmt.Sprintf("run `%s --version`, check you are signed in, and re-run `archwright doctor`", name))
		return
	}
	notices := "none"
	if len(res.Notices) > 0 {
		notices = strings.Join(res.Notices, " | ")
		r.warn("managed-policy:"+name, "the runner reported managed-policy notices: "+notices,
			"nothing to fix here: the factory never overrides managed policy (ADR 0011); read them in case one blocks a Guild")
	} else {
		r.pass("managed-policy:"+name, "no managed-policy notices reported")
	}

	if len(res.Denied) > 0 {
		cmds := make([]string, 0, len(res.Denied))
		for _, dn := range res.Denied {
			cmds = append(cmds, dn.Command)
		}
		r.fail("tool:"+name, fmt.Sprintf("%s denied %q: `archwright tool` does not run unprompted", name, strings.Join(cmds, "; ")), spec.allowFix)
		r.warn("transport:"+name, "not verified: the call was denied before it could use the spool", "fix the permission above, then re-run `archwright doctor`")
		return
	}
	r.pass("tool:"+name, "%s reported no permission prompt for `archwright tool`", name)

	switch {
	case !res.RequestSeen:
		r.fail("transport:"+name, fmt.Sprintf("no request reached the spool: from inside %s's sandbox `archwright tool` wrote nothing under %s", name, root),
			fmt.Sprintf("make sure %s can write under %s in its sandbox (it is %s's own temp area, ADR 0018) and that `archwright` is on the PATH %s runs with", root, name, name, name))
	case !res.ResponseRead:
		r.fail("transport:"+name, fmt.Sprintf("the request reached the spool but the response was not read back from inside %s's sandbox", name),
			fmt.Sprintf("make sure %s's sandbox can read files under %s; spool access must allow both write and read", name, root))
	default:
		r.pass("transport:"+name, "request and response travelled through the spool under %s from inside the sandbox", root)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
