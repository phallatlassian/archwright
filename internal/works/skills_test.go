package works

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/skills"
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
