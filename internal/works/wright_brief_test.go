package works

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/skills"
)

// A read-only Wright runs in plan mode and read that as "present findings, do
// not act": it wrote its review as chat and never called report_result, so
// the step failed. Its brief says plan mode allows the call and that the call
// is how it ends.
func TestWrightBriefSaysPlanModeAllowsReportResult(t *testing.T) {
	b, err := parseBrief(json.RawMessage(goodBrief))
	if err != nil {
		t.Fatal(err)
	}
	got := b.Render("hallmark", "keep it small", nil, 0, "")
	for _, want := range []string{"plan mode", "always allowed", "recorded as failed", "report_result"} {
		if !strings.Contains(got, want) {
			t.Fatalf("brief lacks %q:\n%s", want, got)
		}
	}
}

var tddAndPR = []skills.Info{
	{Name: "tdd", Description: "Test-driven development."},
	{Name: "pr", Description: "Use when writing a PR body."},
}

// A Wright is given its Guild's skills but nothing told it so: no run called
// one (#160). Its brief lists the invocable ones and says to use them.
func TestWrightBriefListsTheSkillsItCanInvoke(t *testing.T) {
	b, err := parseBrief(json.RawMessage(goodBrief))
	if err != nil {
		t.Fatal(err)
	}
	got := b.Render("masonry", "keep it small", tddAndPR, 0, "")
	for _, want := range []string{
		"## Skills\n",
		"- `tdd`: Test-driven development.\n",
		"- `pr`: Use when writing a PR body.\n",
		"before you start, if one of these fits what you are about to do, invoke it with the Skill tool and follow it",
		"name the skills you used in your handback, under `observations` or `evidence`",
	} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Errorf("brief lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n- `") < 2 || strings.Contains(got, "`implement`") {
		t.Errorf("brief lists skills it was not given:\n%s", got)
	}
	if strings.Index(got, "## Skills") < strings.Index(got, "Guild standards") {
		t.Errorf("the Skills section comes before the Guild standards:\n%s", got)
	}
	if none := b.Render("masonry", "keep it small", nil, 0, ""); strings.Contains(none, "Skills") {
		t.Errorf("a Guild with no skills got a Skills section:\n%s", none)
	}
	if rework := b.Render("masonry", "x", tddAndPR, 1, "wrong"); !strings.Contains(rework, "## Skills") {
		t.Errorf("a rework brief lacks the Skills section:\n%s", rework)
	}
}

func TestGuildsFromRegistryListsOnlyTheSkillsTheAgentCanInvoke(t *testing.T) {
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	src := GuildsFromRegistry(reg, DefaultProfiles(), skills.Vendored())

	g, err := src("masonry")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range g.Skills {
		names = append(names, s.Name)
		if strings.TrimSpace(s.Description) == "" || strings.Contains(s.Description, "\n") {
			t.Errorf("skill %s has no one-line description: %q", s.Name, s.Description)
		}
	}
	if got, want := strings.Join(names, ","), "implement,implement-spec,tdd,codebase-design,pr,diagnosing-bugs"; got != want {
		t.Errorf("masonry skills = %s, want %s", got, want)
	}
	for _, guild := range []string{"survey", "hallmark"} {
		g, err := src(guild)
		if err != nil || len(g.Skills) != 1 {
			t.Errorf("%s: %+v %v", guild, g.Skills, err)
		}
	}
	// Chancery's forks are invocable (#163): all are listed.
	if g, err := src("chancery"); err != nil || len(g.Skills) != 6 {
		t.Errorf("chancery skills = %+v %v", g.Skills, err)
	}
	// With no catalogue there is nothing to list.
	if g, _ := GuildsFromRegistry(reg, DefaultProfiles(), nil)("masonry"); len(g.Skills) != 0 {
		t.Errorf("skills without a catalogue: %+v", g.Skills)
	}
}
