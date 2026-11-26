package works

import (
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

// Agents reach the Forge themselves with plain gh and git (ADR 0028), so the
// briefs they start from must say so, point them to record_pr for the one
// thing the Works needs told, and ask for one simple command per Bash call.
// These tests check the briefs the stub runners receive; whether a real agent
// obeys is for a run on a fixture Site.

// removedForgeTools are the brokered tools no brief may still send an agent to.
var removedForgeTools = []string{"read_issue", "push_branch", "open_pr", "read_pr", "comment_pr", "merge_base"}

// assertAgentRules checks that text carries the Forge and command guidance
// and sends nobody to a tool that no longer exists.
func assertAgentRules(t *testing.T, who, text string) {
	t.Helper()
	for _, want := range []string{
		"`gh issue view <URL>`", "Never push, open a pull request or comment on one",
		"one simple command per Bash call", "no pipes, heredocs, `cd`, command substitution",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s brief lacks %q:\n%s", who, want, text)
		}
	}
	assertNoRemovedForgeTool(t, who, text)
}

func assertNoRemovedForgeTool(t *testing.T, who, text string) {
	t.Helper()
	for _, name := range removedForgeTools {
		if strings.Contains(text, name) {
			t.Errorf("%s brief still names the removed tool %s", who, name)
		}
	}
	if strings.Contains(text, "fail in your sandbox") || strings.Contains(text, "Never run `gh`") {
		t.Errorf("%s brief still says gh and git push are unavailable", who)
	}
}

func TestWardenStepBriefTellsTheWardenToUseGhAndRecordThePR(t *testing.T) {
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
	assertNoRemovedForgeTool(t, "Warden", brief)
	for _, want := range []string{
		"`gh` and `git` yourself", "`gh issue view <URL>`", "`record_pr`", `{"url":"<the PR's URL>","number":<its number>}`,
		"Review before it is Plan ordering only", "one simple command per Bash call",
		"has no `Source issue` line", "blocking `ask` of kind `clarification` for the issue URL",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("Warden brief lacks %q", want)
		}
	}
	if _, ok := catalogue["record_pr"]; !ok {
		t.Error("record_pr is not in the catalogue")
	}
}

func TestStepBriefNamesTheJobsSourceIssue(t *testing.T) {
	job := store.Job{ID: "job-1", Title: "Fix login", SourceIssue: "https://github.com/acme/app/issues/7"}
	brief := renderStepBrief(job, store.Step{ID: "build", Title: "Build"}, Guild{Name: "masonry"}, "", "")
	if !strings.Contains(brief, "Source issue: https://github.com/acme/app/issues/7") {
		t.Errorf("the step brief does not name the source issue:\n%s", brief)
	}
	job.SourceIssue = ""
	if brief := renderStepBrief(job, store.Step{ID: "build", Title: "Build"}, Guild{Name: "masonry"}, "", ""); strings.Contains(brief, "\nSource issue: ") {
		t.Errorf("the step brief names a source issue the Job has not:\n%s", brief)
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
	if _, ok := catalogue["run_gate"]; ok {
		t.Error("run_gate is still in the Works tool catalogue")
	}
	for _, want := range []string{"Run the Site's checks", "in your own shell", "Commit your work to your branch"} {
		if !strings.Contains(reqs[0].Brief, want) {
			t.Errorf("Wright brief lacks %q", want)
		}
	}
	for _, banned := range []string{"run_gate", "Never run `git add`", "Never run `go`"} {
		if strings.Contains(reqs[0].Brief, banned) {
			t.Errorf("Wright brief still says %q", banned)
		}
	}
}

func TestReviewBriefCarriesTheAgentRules(t *testing.T) {
	brief := renderReviewBrief(Guild{Name: "hallmark"}, store.Review{Seq: 1, SHA: "abc", Branch: "b"}, "/wt", "spec")
	assertAgentRules(t, "review Warden", brief)
}
