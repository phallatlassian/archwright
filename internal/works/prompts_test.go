package works

import (
	"regexp"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

// The Works brokers every Forge action (ADR 0022), so the briefs agents start
// from must send them to the Forge tools and away from gh and git push, and
// ask for one simple command per Bash call. These tests check the briefs the
// stub runners receive; whether a real agent obeys is for a run on a fixture
// Site.

// ghWord matches gh as a command word, not inside another word.
var ghWord = regexp.MustCompile(`(^|[^A-Za-z])gh([^A-Za-z]|$)`)

// assertAgentRules checks that text carries the Forge and command guidance
// and that every mention of gh or git push is a prohibition.
func assertAgentRules(t *testing.T, who, text string) {
	t.Helper()
	for _, want := range []string{
		"Never run `gh`", "`git push`", "one simple command per Bash call",
		"no pipes, heredocs, `cd`, command substitution",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s brief lacks %q:\n%s", who, want, text)
		}
	}
	assertNoForgeShellInstruction(t, who, text)
}

// assertNoForgeShellInstruction fails on any line that tells the agent to run
// gh or git push: a line naming either must also forbid it.
func assertNoForgeShellInstruction(t *testing.T, who, text string) {
	t.Helper()
	if strings.Contains(text, "gh pr create") {
		t.Errorf("%s mentions gh pr create", who)
	}
	for _, line := range strings.Split(text, "\n") {
		if !ghWord.MatchString(line) && !strings.Contains(line, "git push") {
			continue
		}
		if l := strings.ToLower(line); !strings.Contains(l, "never") && !strings.Contains(l, "do not") {
			t.Errorf("%s has a line that does not forbid gh or git push: %q", who, line)
		}
	}
}

func TestWardenStepBriefSendsAgentsToTheForgeTools(t *testing.T) {
	g := newStepRig(t, true)
	if resp := g.start("a"); !resp.OK {
		t.Fatalf("start: %+v", resp)
	}
	g.waitStatus("a", store.StepHandedBack)
	reqs := g.warden.Requests()
	if len(reqs) != 1 {
		t.Fatalf("warden requests: %+v", reqs)
	}
	brief := reqs[0].Brief
	assertAgentRules(t, "Warden", brief)
	// every Forge tool the registry defines is named, and the argument shapes are the tools' own
	for name := range forgeTools {
		if !strings.Contains(brief, "`"+name+"`") {
			t.Errorf("Warden brief does not name the Forge tool %s", name)
		}
	}
	if _, ok := catalogue["merge_base"]; !ok || !strings.Contains(brief, "`merge_base`") {
		t.Error("Warden brief does not name merge_base")
	}
	for _, want := range []string{`{"title":"<one line>","body":"<markdown>"}`, `{"body":"<markdown>"}`} {
		if !strings.Contains(brief, want) {
			t.Errorf("Warden brief lacks the argument shape %s", want)
		}
	}
}

func TestWrightBriefCarriesTheAgentRules(t *testing.T) {
	g := newRig(t, nil)
	g.dispatch("login")
	reqs := g.runner.Requests()
	if len(reqs) != 1 {
		t.Fatalf("wright requests: %d", len(reqs))
	}
	assertAgentRules(t, "Wright", reqs[0].Brief)
	if !strings.Contains(reqs[0].Brief, "archwright tool read_issue") {
		t.Error("Wright brief does not name read_issue")
	}
	if _, ok := catalogue["run_gate"]; !ok || !strings.Contains(reqs[0].Brief, "archwright tool run_gate") {
		t.Error("Wright brief does not name run_gate")
	}
}

func TestReviewBriefCarriesTheAgentRules(t *testing.T) {
	brief := renderReviewBrief(Guild{Name: "hallmark"}, store.Review{Seq: 1, SHA: "abc", Branch: "b"}, "/wt", "spec")
	assertAgentRules(t, "review Warden", brief)
	if strings.Contains(brief, "run_gate") {
		t.Error("a review Warden cannot call run_gate, yet its brief names it")
	}
}
