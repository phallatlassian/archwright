package sites

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/store"
)

// pieceJob is a Job with an integration worktree and one Wright worktree.
func pieceJob(t *testing.T, e env) (store.Job, store.Worktree, store.Worktree) {
	t.Helper()
	j := e.job(t)
	integ, err := e.m.CreateWorktree(j.ID, store.KindIntegration, "")
	if err != nil {
		t.Fatal(err)
	}
	w, err := e.m.CreateWorktree(j.ID, store.KindWright, "")
	if err != nil {
		t.Fatal(err)
	}
	return j, integ, w
}

func commitFile(t *testing.T, e env, j store.Job, dir, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "--", rel)
	run(t, dir, "commit", "--quiet", "-m", "edit "+rel)
}

func TestMergePieceFastForwardsAndMerges(t *testing.T) {
	e := setup(t)
	j, integ, w := pieceJob(t, e)
	commitFile(t, e, j, w.Path, "b.txt", "b")
	got, err := e.m.MergePiece(j.ID, w.Branch)
	if err != nil || got.Status != PieceMerged {
		t.Fatalf("%+v %v", got, err)
	}
	if b := run(t, integ.Path, "show", "HEAD:b.txt"); b != "b" {
		t.Errorf("integration lacks the piece's file: %q", b)
	}
	if n := run(t, integ.Path, "rev-list", "--count", "--merges", "HEAD"); n != "0" {
		t.Errorf("the integration branch had not moved, so this is a fast-forward; merges %s", n)
	}

	// the integration branch moves on; a second piece from the old base needs a merge commit
	w2, err := e.m.CreateWorktree(j.ID, store.KindWright, "")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, e, j, w2.Path, "c.txt", "c")
	commitFile(t, e, j, integ.Path, "d.txt", "d")
	if got, err = e.m.MergePiece(j.ID, w2.Branch); err != nil || got.Status != PieceMerged {
		t.Fatalf("%+v %v", got, err)
	}
	if n := run(t, integ.Path, "rev-list", "--count", "--merges", "HEAD"); n != "1" {
		t.Errorf("merge commits %s, want 1", n)
	}
	for _, f := range []string{"b.txt", "c.txt", "d.txt"} {
		if _, err := os.Stat(filepath.Join(integ.Path, f)); err != nil {
			t.Errorf("%s missing: %v", f, err)
		}
	}
}

func TestMergePieceWithNoCommitsOrAlreadyMergedChangesNothing(t *testing.T) {
	e := setup(t)
	j, integ, w := pieceJob(t, e)
	before := run(t, integ.Path, "rev-parse", "HEAD")
	got, err := e.m.MergePiece(j.ID, w.Branch)
	if err != nil || got.Status != PieceUnchanged || got.Tip != before {
		t.Fatalf("%+v %v", got, err)
	}
	commitFile(t, e, j, w.Path, "b.txt", "b")
	if got, err = e.m.MergePiece(j.ID, w.Branch); err != nil || got.Status != PieceMerged {
		t.Fatalf("%+v %v", got, err)
	}
	merged := run(t, integ.Path, "rev-parse", "HEAD")
	if got, err = e.m.MergePiece(j.ID, w.Branch); err != nil || got.Status != PieceUnchanged {
		t.Fatalf("again: %+v %v", got, err)
	}
	if after := run(t, integ.Path, "rev-parse", "HEAD"); after != merged {
		t.Error("merging twice moved the branch")
	}
}

func TestMergePieceConflictLeavesTheIntegrationBranchAsItWas(t *testing.T) {
	e := setup(t)
	j, integ, w := pieceJob(t, e)
	commitFile(t, e, j, w.Path, "a.txt", "from the piece")
	commitFile(t, e, j, integ.Path, "a.txt", "from the integration branch")
	before := run(t, integ.Path, "rev-parse", "HEAD")
	got, err := e.m.MergePiece(j.ID, w.Branch)
	if err != nil || got.Status != PieceConflict || len(got.Conflicts) != 1 || got.Conflicts[0] != "a.txt" {
		t.Fatalf("%+v %v", got, err)
	}
	if after := run(t, integ.Path, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved: %s -> %s", before, after)
	}
	if st := run(t, integ.Path, "status", "--porcelain"); st != "" {
		t.Errorf("worktree is not clean after the abort:\n%s", st)
	}
	if b, _ := os.ReadFile(filepath.Join(integ.Path, "a.txt")); string(b) != "from the integration branch" {
		t.Errorf("a.txt = %q", b)
	}
}

func TestMergePieceRefusesWhatIsNotAWrightBranchOfTheJob(t *testing.T) {
	e := setup(t)
	j, integ, w := pieceJob(t, e)
	other := e.job(t)
	for _, branch := range []string{integ.Branch, "main", "origin/main", "", "-x"} {
		if _, err := e.m.MergePiece(j.ID, branch); err == nil {
			t.Errorf("%q was merged", branch)
		}
	}
	if _, err := e.m.MergePiece(other.ID, w.Branch); err == nil {
		t.Error("another Job's Wright branch was merged")
	}
}

func TestMergePieceRefusesADirtyIntegrationWorktree(t *testing.T) {
	e := setup(t)
	j, integ, w := pieceJob(t, e)
	commitFile(t, e, j, w.Path, "b.txt", "b")
	if err := os.WriteFile(filepath.Join(integ.Path, "a.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.MergePiece(j.ID, w.Branch); err == nil {
		t.Fatal("merged into a dirty worktree")
	}
}

func TestMergePieceIgnoresSkillsDeliveredIntoTheIntegrationWorktree(t *testing.T) {
	e := setup(t)
	j, integ, w := pieceJob(t, e)
	commitFile(t, e, j, w.Path, "b.txt", "b")
	set := []skills.Skill{{Name: "demo", FS: fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: demo\n---\n")}}}}
	if _, err := skills.Deliver(skills.Request{Worktree: integ.Path, RunDir: filepath.Join(realTemp(t), "run"), Runner: "claude", Skills: set}); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.MergePiece(j.ID, w.Branch)
	if err != nil || got.Status != PieceMerged {
		t.Fatalf("merge = %+v, %v; want merged", got, err)
	}
}

func TestEnsureIntegrationIsIdempotentAndEnablesMerge(t *testing.T) {
	e := setup(t)
	j := e.job(t)
	w, err := e.m.CreateWorktree(j.ID, store.KindWright, "")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, e, j, w.Path, "b.txt", "b")
	if _, err := e.m.MergePiece(j.ID, w.Branch); err == nil {
		t.Fatal("merge without an integration worktree should conflict")
	}
	for i := 0; i < 2; i++ {
		if err := e.m.EnsureIntegration(j.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.m.MergePiece(j.ID, w.Branch)
	if err != nil || got.Status != PieceMerged {
		t.Fatalf("merge: %+v %v", got, err)
	}
}
