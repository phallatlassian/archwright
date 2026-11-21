package sites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

func TestCommitWorktreeCommitsTrackedAndUntrackedButNeverScaffolding(t *testing.T) {
	e := setup(t)
	j, err := e.st.CreateJobOn("demo", "T", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CreateWorktree(j.ID, store.KindIntegration, ""); err != nil {
		t.Fatal(err)
	}
	w, err := e.m.CreateWorktree(j.ID, store.KindWright, "")
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		p := filepath.Join(w.Path, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "edited")
	write("new/b.txt", "new")
	write(".claude/settings.json", "{}")
	write(".agents/skills/x/SKILL.md", "x")

	sha, err := e.m.CommitWorktree(j.ID, w.Path, "piece p: done")
	if err != nil || sha == "" {
		t.Fatalf("sha %q err %v", sha, err)
	}
	if got := run(t, w.Path, "rev-parse", "HEAD"); got != sha {
		t.Errorf("HEAD %s, want %s", got, sha)
	}
	files := run(t, w.Path, "show", "--name-only", "--format=%s", "HEAD")
	if !strings.Contains(files, "piece p: done") || !strings.Contains(files, "a.txt") || !strings.Contains(files, "new/b.txt") {
		t.Errorf("commit lacks the work:\n%s", files)
	}
	if strings.Contains(files, ".claude") || strings.Contains(files, ".agents") {
		t.Errorf("scaffolding was committed:\n%s", files)
	}
	if _, err := os.Stat(filepath.Join(w.Path, ".claude", "settings.json")); err != nil {
		t.Errorf("scaffolding was removed from the worktree: %v", err)
	}

	// nothing new, only scaffolding: no empty commit
	before := run(t, w.Path, "rev-parse", "HEAD")
	write(".claude/more.json", "{}")
	if sha, err := e.m.CommitWorktree(j.ID, w.Path, "again"); err != nil || sha != "" {
		t.Errorf("no changes: sha %q err %v", sha, err)
	}
	if after := run(t, w.Path, "rev-parse", "HEAD"); after != before {
		t.Error("an empty commit was made")
	}
}

func TestCommitWorktreeRefusesAWorktreeTheJobDoesNotOwn(t *testing.T) {
	e := setup(t)
	j, _ := e.st.CreateJobOn("demo", "T", "")
	if _, err := e.m.CommitWorktree(j.ID, e.dev, "x"); err == nil {
		t.Error("committed in a directory that is not the Job's worktree")
	}
}
