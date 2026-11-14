package skills

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func tgit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// site is a Site's git worktree with a developer home whose global git
// configuration and excludes must keep working inside a run.
type site struct {
	t        *testing.T
	worktree string
	runDir   string
	home     string
}

// newSite makes a repository holding tracked files, and a linked worktree of
// it, as the Works would for a run.
func newSite(t *testing.T, tracked map[string]string) site {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	write(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = "+filepath.Join(home, ".gitignore_global")+"\n[user]\n\tname = dev\n")
	write(t, filepath.Join(home, ".gitignore_global"), "*.devignored\n")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	os.Unsetenv("GIT_CONFIG_GLOBAL")

	main := filepath.Join(root, "main")
	tgit(t, root, nil, "init", "-q", "-b", "main", main)
	write(t, filepath.Join(main, ".gitignore"), "/build\n")
	write(t, filepath.Join(main, "README.md"), "site\n")
	for p, body := range tracked {
		write(t, filepath.Join(main, p), body)
	}
	tgit(t, main, nil, "add", "-A")
	tgit(t, main, nil, "commit", "-q", "-m", "init")
	wt := filepath.Join(root, "wt")
	tgit(t, main, nil, "worktree", "add", "-q", "--detach", wt)
	return site{t: t, worktree: wt, runDir: filepath.Join(root, "state", "runs", "r1"), home: home}
}

func (s site) skills(names ...string) []Skill {
	var out []Skill
	for _, n := range names {
		out = append(out, Skill{Name: n, FS: fstest.MapFS{
			"SKILL.md":       {Data: []byte("---\nname: " + n + "\n---\nbody\n")},
			"scripts/run.sh": {Data: []byte("#!/bin/sh\necho hi\n")},
			"notes/ref.md":   {Data: []byte("ref\n")},
		}})
	}
	return out
}

func (s site) deliver(runner string, names ...string) (Delivery, error) {
	return Deliver(Request{Worktree: s.worktree, RunDir: s.runDir, Runner: runner, Skills: s.skills(names...)})
}

// runGit runs git as the run would: inheriting the Works-supplied environment.
func (s site) runGit(d Delivery, args ...string) string {
	var env []string
	for k, v := range d.Env {
		env = append(env, k+"="+v)
	}
	return tgit(s.t, s.worktree, env, args...)
}

func TestDeliverBothRunnersSeeSkills(t *testing.T) {
	for _, runner := range []string{"claude", "codex"} {
		t.Run(runner, func(t *testing.T) {
			s := newSite(t, nil)
			if _, err := s.deliver(runner, "tdd", "implement"); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"tdd", "implement"} {
				if got := read(t, filepath.Join(s.worktree, ".agents/skills", name, "SKILL.md")); !strings.Contains(got, "name: "+name) {
					t.Errorf("codex path for %s: %q", name, got)
				}
				if got := read(t, filepath.Join(s.worktree, ".agents/skills", name, "notes/ref.md")); got != "ref\n" {
					t.Errorf("nested file not copied: %q", got)
				}
			}
			info, err := os.Stat(filepath.Join(s.worktree, ".agents/skills/tdd/scripts/run.sh"))
			if err != nil || info.Mode()&0o111 == 0 {
				t.Errorf("a script keeps its executable bit: %v %v", info, err)
			}
			claude := filepath.Join(s.worktree, ".claude/skills")
			_, err = os.Lstat(claude)
			if runner == "claude" {
				if err != nil {
					t.Fatalf(".claude/skills missing: %v", err)
				}
				if link, _ := os.Readlink(claude); link != "../.agents/skills" {
					t.Errorf(".claude/skills links to %q", link)
				}
				if got := read(t, filepath.Join(claude, "tdd", "SKILL.md")); !strings.Contains(got, "name: tdd") {
					t.Errorf("claude path: %q", got)
				}
			} else if err == nil {
				t.Error("codex runs get no .claude/skills")
			}
		})
	}
}

func TestDeliverHidesFactoryFilesFromGit(t *testing.T) {
	for _, runner := range []string{"claude", "codex"} {
		t.Run(runner, func(t *testing.T) {
			s := newSite(t, nil)
			d, err := s.deliver(runner, "tdd")
			if err != nil {
				t.Fatal(err)
			}
			if got := s.runGit(d, "status", "--porcelain"); got != "" {
				t.Errorf("git status is not clean:\n%s", got)
			}
			s.runGit(d, "add", "-A")
			if got := s.runGit(d, "diff", "--cached", "--name-only"); got != "" {
				t.Errorf("git add -A staged factory files:\n%s", got)
			}
			// The developer's own global excludes still apply to the run.
			write(t, filepath.Join(s.worktree, "scratch.devignored"), "x")
			write(t, filepath.Join(s.worktree, "real.txt"), "x")
			if got := s.runGit(d, "status", "--porcelain"); got != "?? real.txt" {
				t.Errorf("developer excludes lost or work hidden: %q", got)
			}
			// Control: without the Works' environment the files show up.
			if got := tgit(t, s.worktree, nil, "status", "--porcelain"); !strings.Contains(got, ".agents") {
				t.Errorf("control: expected untracked factory files, got %q", got)
			}
		})
	}
}

func TestDeliverNeverTouchesTheSitesGitFiles(t *testing.T) {
	s := newSite(t, nil)
	common := tgit(t, s.worktree, nil, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(common) {
		common = filepath.Join(s.worktree, common)
	}
	gitdir := tgit(t, s.worktree, nil, "rev-parse", "--absolute-git-dir")
	before := map[string]string{}
	for _, p := range []string{filepath.Join(common, "config"), filepath.Join(common, "info", "exclude"), filepath.Join(s.worktree, ".gitignore"), filepath.Join(gitdir, "info", "exclude"), filepath.Join(s.home, ".gitconfig"), filepath.Join(s.home, ".gitignore_global")} {
		if b, err := os.ReadFile(p); err == nil {
			before[p] = string(b)
		}
	}
	headBefore := tgit(t, s.worktree, nil, "rev-parse", "HEAD")
	d, err := s.deliver("claude", "tdd", "implement")
	if err != nil {
		t.Fatal(err)
	}
	s.runGit(d, "add", "-A")
	for p, want := range before {
		if got := read(t, p); got != want {
			t.Errorf("%s changed:\n%s", p, got)
		}
	}
	if got := tgit(t, s.worktree, nil, "rev-parse", "HEAD"); got != headBefore {
		t.Error("HEAD moved")
	}
	if got := tgit(t, s.worktree, nil, "ls-files"); got != ".gitignore\nREADME.md" {
		t.Errorf("tracked files changed: %q", got)
	}
}

func TestDeliverEnvPointsAtGeneratedConfigOutsideTheWorktree(t *testing.T) {
	s := newSite(t, nil)
	d, err := s.deliver("claude", "tdd")
	if err != nil {
		t.Fatal(err)
	}
	cfg := d.Env["GIT_CONFIG_GLOBAL"]
	if cfg != filepath.Join(s.runDir, "gitconfig") {
		t.Fatalf("GIT_CONFIG_GLOBAL = %q", cfg)
	}
	body := read(t, cfg)
	if !strings.Contains(body, filepath.Join(s.home, ".gitconfig")) {
		t.Errorf("the developer's global config is not included:\n%s", body)
	}
	excludes := read(t, filepath.Join(s.runDir, "excludes"))
	if !strings.Contains(excludes, "*.devignored") || !strings.Contains(excludes, "/.agents/skills\n") || !strings.Contains(excludes, "/.claude/skills\n") {
		t.Errorf("excludes:\n%s", excludes)
	}
	if got := s.runGit(d, "config", "--global", "--includes", "user.name"); got != "dev" {
		t.Errorf("the developer's own settings are lost inside the run: %q", got)
	}
}

func TestDeliverRefusesACollisionWithATrackedSkill(t *testing.T) {
	cases := map[string]struct {
		tracked map[string]string
		runner  string
	}{
		"agents":          {map[string]string{".agents/skills/tdd/SKILL.md": "site's own"}, "codex"},
		"claude":          {map[string]string{".claude/skills/tdd/SKILL.md": "site's own"}, "claude"},
		"claude symlink":  {map[string]string{".claude/skills/tdd": "x"}, "claude"},
		"agents for both": {map[string]string{".agents/skills/tdd/SKILL.md": "site's own"}, "claude"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSite(t, c.tracked)
			_, err := s.deliver(c.runner, "implement", "tdd")
			var ce *CollisionError
			if !errors.As(err, &ce) || !errors.Is(err, ErrCollision) {
				t.Fatalf("err = %v, want a CollisionError", err)
			}
			if ce.Skill != "tdd" {
				t.Errorf("collision names skill %q", ce.Skill)
			}
			if !strings.Contains(err.Error(), "tdd") || !strings.Contains(err.Error(), "tracks") {
				t.Errorf("message is not visible enough: %v", err)
			}
			if _, err := os.Stat(filepath.Join(s.worktree, ".agents/skills/implement")); err == nil && c.tracked[".agents/skills/tdd/SKILL.md"] == "" {
				t.Error("a collision must fail before anything is written")
			}
			if _, err := os.Stat(s.runDir); err == nil {
				t.Error("a collision must fail before the run's git files are generated")
			}
			if got := tgit(t, s.worktree, nil, "status", "--porcelain"); got != "" {
				t.Errorf("the worktree was dirtied: %q", got)
			}
		})
	}
}

func TestDeliverAddsBesideTheSitesOwnSkills(t *testing.T) {
	s := newSite(t, map[string]string{
		".agents/skills/site-skill/SKILL.md": "site's",
		".claude/skills/site-skill/SKILL.md": "site's",
		".claude/skills/another/SKILL.md":    "site's",
	})
	d, err := s.deliver("claude", "tdd")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.runGit(d, "status", "--porcelain"); got != "" {
		t.Errorf("git status:\n%s", got)
	}
	s.runGit(d, "add", "-A")
	if got := s.runGit(d, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("staged:\n%s", got)
	}
	link, err := os.Readlink(filepath.Join(s.worktree, ".claude/skills/tdd"))
	if err != nil || link != "../../.agents/skills/tdd" {
		t.Errorf("per-skill link = %q, %v", link, err)
	}
	if got := read(t, filepath.Join(s.worktree, ".claude/skills/tdd/SKILL.md")); !strings.Contains(got, "name: tdd") {
		t.Errorf("claude cannot see the skill: %q", got)
	}
	if got := read(t, filepath.Join(s.worktree, ".claude/skills/site-skill/SKILL.md")); got != "site's" {
		t.Errorf("the Site's own skill was touched: %q", got)
	}
	excludes := read(t, filepath.Join(s.runDir, "excludes"))
	if strings.Contains(excludes, "/.claude/skills\n") || strings.Contains(excludes, "/.agents/skills\n") {
		t.Errorf("must not hide the Site's own skills directories:\n%s", excludes)
	}
}

func TestDeliverTwiceIntoTheSameWorktree(t *testing.T) {
	s := newSite(t, nil)
	if _, err := s.deliver("claude", "tdd", "implement"); err != nil {
		t.Fatal(err)
	}
	s.runDir = filepath.Join(filepath.Dir(s.runDir), "r2")
	d, err := s.deliver("claude", "tdd")
	if err != nil {
		t.Fatalf("a second run in the worktree: %v", err)
	}
	if got := s.runGit(d, "status", "--porcelain"); got != "" {
		t.Errorf("git status:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(s.worktree, ".agents/skills/implement")); err == nil {
		t.Error("the previous run's copy is replaced, not merged")
	}
}

func TestDeliverRefusesBadRequests(t *testing.T) {
	s := newSite(t, nil)
	for name, req := range map[string]Request{
		"run dir inside worktree": {Worktree: s.worktree, RunDir: filepath.Join(s.worktree, ".run"), Runner: "claude", Skills: s.skills("tdd")},
		"bad skill name":          {Worktree: s.worktree, RunDir: s.runDir, Runner: "claude", Skills: s.skills("../x")},
		"duplicate skill":         {Worktree: s.worktree, RunDir: s.runDir, Runner: "claude", Skills: s.skills("tdd", "tdd")},
		"no worktree":             {RunDir: s.runDir, Runner: "claude", Skills: s.skills("tdd")},
		"unknown runner":          {Worktree: s.worktree, RunDir: s.runDir, Runner: "vim", Skills: s.skills("tdd")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Deliver(req); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestDeliverWithNoSkillsDoesNothing(t *testing.T) {
	s := newSite(t, nil)
	d, err := s.deliver("claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Env) != 0 {
		t.Errorf("env = %v", d.Env)
	}
	if _, err := os.Stat(filepath.Join(s.worktree, ".agents")); err == nil {
		t.Error("nothing to deliver, nothing written")
	}
}

func TestDeliverRefusesASiteExcludesFileOverride(t *testing.T) {
	for _, scope := range []string{"--local", "--worktree"} {
		t.Run(scope, func(t *testing.T) {
			s := newSite(t, nil)
			if scope == "--worktree" {
				tgit(t, s.worktree, nil, "config", "extensions.worktreeConfig", "true")
			}
			tgit(t, s.worktree, nil, "config", scope, "core.excludesFile", filepath.Join(s.home, "site_excludes"))
			_, err := s.deliver("claude", "tdd")
			if err == nil || !strings.Contains(err.Error(), "core.excludesFile") {
				t.Fatalf("err = %v, want one naming core.excludesFile", err)
			}
			if _, err := os.Stat(filepath.Join(s.worktree, ".agents")); err == nil {
				t.Error("nothing may be written when hiding cannot work")
			}
		})
	}
}
