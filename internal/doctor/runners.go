package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/phallatlassian/archwright/internal/launch"
)

type runnerSpec struct {
	name    string
	install string
}

var runnerSpecs = []runnerSpec{
	{name: launch.RunnerClaude, install: "install Claude Code so `claude` is on PATH (https://claude.com/claude-code)"},
	{name: launch.RunnerCodex, install: "install the Codex CLI so `codex` is on PATH (npm install -g @openai/codex)"},
}

// checkRunner checks one runner: that its CLI is present and answers
// `--version`. Whether a Session of it starts, and whether its hooks reach the
// Works, is the Site trust probe's to show (see checkTrust). A runner the
// install does not use is only a warning when absent; an installed one must pass.
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
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
