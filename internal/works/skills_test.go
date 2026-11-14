package works

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/store"
)

func TestDeliverSkillsForAShippedGuild(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	for _, args := range [][]string{{"init", "-q", wt}, {"-C", wt, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "i"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g, _ := reg.Guild("masonry")

	env, err := DeliverSkills(skills.Vendored(), g, "claude", wt, filepath.Join(root, "run"))
	if err != nil {
		t.Fatal(err)
	}
	if env["GIT_CONFIG_GLOBAL"] == "" {
		t.Error("no GIT_CONFIG_GLOBAL")
	}
	for _, name := range g.Skills {
		if _, err := os.Stat(filepath.Join(wt, ".claude/skills", name, "SKILL.md")); err != nil {
			t.Errorf("claude cannot see %s: %v", name, err)
		}
	}

	// A Site tracking a same-named skill fails the run with the collision in the message.
	if err := os.WriteFile(filepath.Join(wt, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	site := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", site).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	if err := os.MkdirAll(filepath.Join(site, ".agents/skills/tdd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, ".agents/skills/tdd/SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", site, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	_, err = DeliverSkills(skills.Vendored(), g, "codex", site, filepath.Join(root, "run2"))
	if !errors.Is(err, skills.ErrCollision) || !strings.Contains(err.Error(), "masonry") {
		t.Errorf("err = %v", err)
	}
}

func TestDispatchDeliversSkillsAndFailsOnCollision(t *testing.T) {
	root := t.TempDir()
	mkSite := func(name string, trackSkill bool) string {
		wt := filepath.Join(root, name)
		if out, err := exec.Command("git", "init", "-q", wt).CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
		if trackSkill {
			if err := os.MkdirAll(filepath.Join(wt, ".agents/skills/tdd"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt, ".agents/skills/tdd/SKILL.md"), []byte("site's own"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("git", "-C", wt, "add", "-A").CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
		return wt
	}
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()

	run := func(wt string) (*rig, store.Dispatch) {
		g := newRig(t, nil)
		g.r.UseDefinitions(reg)
		g.r.UseSkills(SkillsDelivery{
			Catalogue: skills.Vendored(),
			Runner:    "claude",
			RunDir:    func(id string) string { return filepath.Join(root, "runs", wt[len(root):], id) },
			Worktree:  func(store.Dispatch) (string, error) { return wt, nil },
		})
		g.ok(g.warden, "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`, nil)
		return g, g.waitReported("p")
	}

	g, d := run(mkSite("clean", false))
	reqs := g.runner.Requests()
	if len(reqs) != 1 || reqs[0].ExtraEnv["GIT_CONFIG_GLOBAL"] == "" {
		t.Fatalf("the Wright did not get the run's git configuration: %+v", reqs)
	}
	if _, err := os.Stat(filepath.Join(root, "clean/.claude/skills/tdd/SKILL.md")); err != nil {
		t.Errorf("the Wright's worktree has no skills: %v", err)
	}
	if strings.Contains(string(d.Result), "collides") {
		t.Errorf("clean run failed: %s", d.Result)
	}

	g, d = run(mkSite("tracked", true))
	if len(g.runner.Requests()) != 0 {
		t.Error("the Wright ran despite the collision")
	}
	var res WrightResult
	if err := json.Unmarshal(d.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != ResultFailed || !strings.Contains(res.Uncertainty, `skill "tdd" collides`) {
		t.Errorf("the dispatch did not fail visibly with the collision: %s", d.Result)
	}
}
