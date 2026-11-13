package works

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

var onePieceSteps = []store.Step{{ID: "a", Guild: "engineering", Title: "do it"}}

func TestAdHocPlanGatesDispatchJigPlanDoesNot(t *testing.T) {
	g := newRig(t, map[string][]WrightResult{"p": {doneResult}, "q": {doneResult}})
	req := `{"piece":"p","brief":` + goodBrief + `}`

	// No Plan: a small job goes straight to its Guild.
	g.ok(g.warden, "dispatch_wright", `{"piece":"q","brief":`+goodBrief+`}`, nil)

	if _, err := g.st.SetPlan(g.job.ID, onePieceSteps); err != nil {
		t.Fatal(err)
	}
	g.fails(g.warden, "dispatch_wright", req, spool.CodeConflict)
	if _, err := g.st.DecidePlan(g.job.ID, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	g.fails(g.warden, "dispatch_wright", req, spool.CodeConflict)

	if _, err := g.st.SetPlan(g.job.ID, onePieceSteps); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.DecidePlan(g.job.ID, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	g.ok(g.warden, "dispatch_wright", req, nil)
}

func TestJigPlanDispatchesWithoutApproval(t *testing.T) {
	g := newRig(t, map[string][]WrightResult{"p": {doneResult}})
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g.r.UseDefinitions(reg)
	var plan store.Plan
	g.ok(g.arch, "start_jig", `{"job":"`+g.job.ID+`","jig":"fix","params":{"problem":"login breaks"}}`, &plan)
	if plan.Jig != "fix" || plan.Approval != store.ApprovalNotRequired || len(plan.Steps) != 2 {
		t.Fatalf("%+v", plan)
	}
	g.ok(g.warden, "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`, nil)
	g.fails(g.arch, "start_jig", `{"job":"`+g.job.ID+`","jig":"nope"}`, spool.CodeInvalidInput)
	g.fails(g.arch, "start_jig", `{"job":"`+g.job.ID+`","jig":"fix"}`, spool.CodeInvalidInput) // missing required parameter
	var cat Catalogue
	g.ok(g.arch, "show_catalogue", `{}`, &cat)
	if len(cat.Guilds) != 4 || len(cat.Jigs) != 6 || len(cat.Errors) != 0 {
		t.Errorf("catalogue: %d guilds %d jigs %v", len(cat.Guilds), len(cat.Jigs), cat.Errors)
	}
	g.fails(g.warden, "show_catalogue", `{}`, spool.CodeForbidden)
}

func TestGuildsFromRegistryBindsAndFailsOnUnmetCapability(t *testing.T) {
	cfg := t.TempDir()
	reg := defs.New(defs.Shipped(), cfg)
	reg.Reload()
	g, err := GuildsFromRegistry(reg, DefaultProfiles())("research")
	if err != nil || g.Binding.Access != defs.ReadOnly || !g.Binding.DetachedWorktree || g.Standards == "" {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := GuildsFromRegistry(reg, DefaultProfiles())("nowhere"); !errors.Is(err, errInvalidInput) {
		t.Errorf("unknown guild: %v", err)
	}
	weak := map[string]defs.Profile{"claude": {}, "codex": {}}
	if _, err := GuildsFromRegistry(reg, weak)("research"); !errors.Is(err, errInvalidInput) {
		t.Errorf("read-only guild bound to a runner that cannot enforce it: %v", err)
	}
	// A developer override that widens nothing the runner lacks still loads;
	// an override of an invalid shape does not replace the shipped Guild.
	path := filepath.Join(cfg, "guilds", "research", "GUILD.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("---\nintake: a\nhandback: b\nrunner: claude\nrunners: [claude]\npermission_mode: bypassPermissions\n---\nx"), 0o600)
	if errs := reg.Reload(); len(errs) != 1 {
		t.Fatalf("errors: %v", errs)
	}
	if g, _ := GuildsFromRegistry(reg, DefaultProfiles())("research"); g.Standards == "x" {
		t.Error("invalid override took effect")
	}
}
