package works

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/launch"
)

// boundTo is a guild source whose every Guild runs on the named runner, so a
// run's spool root can be checked against that runner's (ADR 0018).
func boundTo(runnerName string) GuildSource {
	return func(name string) (Guild, error) {
		switch name {
		case "masonry", ReviewGuild:
			return Guild{Name: name, Standards: name + " standards.", Binding: defs.Binding{Guild: name, Runner: runnerName, Access: defs.ReadWrite}}, nil
		}
		return Guild{}, errInvalidInput
	}
}

func wantRoot(runnerName string) string {
	return launch.SpoolRoot(runnerName, os.Getuid(), os.TempDir())
}

func inRoot(spool, root string) bool { return filepath.Dir(spool) == root }

func TestWorksStartedRunsUseTheirRunnersSpoolRoot(t *testing.T) {
	for _, name := range []string{launch.RunnerClaude, launch.RunnerCodex} {
		t.Run("step warden "+name, func(t *testing.T) {
			g := newStepRig(t, true)
			g.r.Configure(boundTo(name), NewFakeRunner(nil))
			if resp := g.start("a"); !resp.OK {
				t.Fatalf("start_step: %+v", resp.Error)
			}
			g.waitStatus("a", "handed_back")
			reqs := g.warden.Requests()
			if len(reqs) != 1 || !inRoot(reqs[0].Env.Spool, wantRoot(name)) {
				t.Fatalf("warden spool = %+v, want a run directory under %s", reqs, wantRoot(name))
			}
		})
		t.Run("wright "+name, func(t *testing.T) {
			g := newRig(t, map[string][]WrightResult{"p": {doneResult}})
			g.r.Configure(boundTo(name), g.runner)
			g.dispatch("p")
			reqs := g.runner.Requests()
			if len(reqs) != 1 || !inRoot(reqs[0].Env.Spool, wantRoot(name)) {
				t.Fatalf("wright spool = %+v, want a run directory under %s", reqs, wantRoot(name))
			}
		})
		t.Run("review "+name, func(t *testing.T) {
			g := newReviewRig(t)
			g.r.Configure(boundTo(name), g.runner)
			g.writ()
			g.commit("a.go", "x")
			g.handBack("p")
			g.review()
			reqs := g.rv.Requests()
			if len(reqs) != 1 || !inRoot(reqs[0].Env.Spool, wantRoot(name)) {
				t.Fatalf("review spool = %+v, want a run directory under %s", reqs, wantRoot(name))
			}
		})
	}
}

func TestExplicitSpoolRootStillWins(t *testing.T) {
	g := newStepRig(t, true)
	g.r.Configure(boundTo(launch.RunnerClaude), NewFakeRunner(nil))
	root := t.TempDir()
	g.r.SetSpoolRoot(root)
	if resp := g.start("a"); !resp.OK {
		t.Fatalf("start_step: %+v", resp.Error)
	}
	g.waitStatus("a", "handed_back")
	if reqs := g.warden.Requests(); len(reqs) != 1 || !inRoot(reqs[0].Env.Spool, root) {
		t.Fatalf("warden spool = %+v, want a run directory under %s", reqs, root)
	}
}
