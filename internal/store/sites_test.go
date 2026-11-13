package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func addSite(t *testing.T, s *Store, name, url string) Site {
	t.Helper()
	site, err := s.AddSite(Site{Name: name, URL: url, ClonePath: "/clones/" + name + ".git", BaseBranch: "main", ConcurrencyCap: 3})
	if err != nil {
		t.Fatal(err)
	}
	return site
}

func TestSitesAreUniqueByNormalisedURL(t *testing.T) {
	s, _ := open(t)
	addSite(t, s, "r", "git@github.com:o/r.git")
	for _, url := range []string{"https://github.com/o/r", "https://github.com/o/r.git", "ssh://git@github.com/o/r.git"} {
		_, err := s.AddSite(Site{Name: "other", URL: url, ClonePath: "/c/other", BaseBranch: "main", ConcurrencyCap: 3})
		if !errors.Is(err, ErrExists) {
			t.Errorf("%s: want ErrExists, got %v", url, err)
		}
	}
	if _, err := s.AddSite(Site{Name: "r", URL: "https://github.com/o/x", ClonePath: "/c/x", BaseBranch: "main", ConcurrencyCap: 3}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate name: %v", err)
	}
	if _, err := s.AddSite(Site{Name: "local", URL: "/srv/git/r.git", ClonePath: "/c/l", BaseBranch: "main", ConcurrencyCap: 3}); err == nil {
		t.Error("local path stored as a Site URL")
	}
	got, err := s.SiteForURL("https://github.com/o/r.git/")
	if err != nil || got.Name != "r" || got.URL != "git@github.com:o/r.git" || got.ClonePath != "/clones/r.git" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, url := range []string{"https://github.com/o/nope", "/some/dir", ""} {
		if _, err := s.SiteForURL(url); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: want ErrNotFound, got %v", url, err)
		}
	}
}

func TestRemovedSiteKeepsJobHistoryAndCanBeRegisteredAgain(t *testing.T) {
	s, _ := open(t)
	addSite(t, s, "r", "https://github.com/o/r")
	j, err := s.CreateJobOn("r", "T", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveSite("r"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListSites(); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
	if _, err := s.SiteForURL("https://github.com/o/r"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed Site resolves: %v", err)
	}
	if _, err := s.GetSite("r"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed Site readable: %v", err)
	}
	if _, err := s.CreateJobOn("r", "T2", ""); err == nil {
		t.Error("job created on a removed Site")
	}
	if got, err := s.GetJob(j.ID); err != nil || got.Site != "r" {
		t.Fatalf("job history lost: %+v %v", got, err)
	}
	if _, err := s.RemoveSite("r"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second removal: %v", err)
	}
	// The same repository can be registered again, under the same name.
	addSite(t, s, "r", "git@github.com:o/r.git")
	if _, err := s.CreateJobOn("r", "T3", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveSiteRefusedWhileWorktreesAreInUse(t *testing.T) {
	s, _ := open(t)
	addSite(t, s, "r", "https://github.com/o/r")
	j, _ := s.CreateJobOn("r", "T", "")
	w, err := s.AddWorktree(Worktree{ID: NewWorktreeID(), JobID: j.ID, Site: "r", Kind: KindIntegration, Path: "/w", Branch: "b", BaseRef: "main", BaseSHA: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveSite("r"); err == nil {
		t.Fatal("removed a Site with live worktrees")
	}
	s.MarkWorktreeRemoved(w, "discarded")
	if _, err := s.RemoveSite("r"); err != nil {
		t.Fatal(err)
	}
}

// A database from before ADR 0019 holds Sites registered by path. They are not
// migrated: they stop resolving, and Job and worktree history survives.
func TestPathRegisteredSitesAreUnregisteredByTheMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archwright.db")
	all, _ := loadMigrations()
	old := loadMigrationsFn
	defer func() { loadMigrationsFn = old }()
	loadMigrationsFn = func() ([]migration, error) { return all[:3], nil } // schema before 0004
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO sites (name, path, base_branch, remote, concurrency_cap, gates, created_at) VALUES ('legacy', '/home/dev/legacy', 'main', 'origin', 3, '{}', 'then'), ('other', '/home/dev/other', 'main', '', 3, '{}', 'then')`,
		`INSERT INTO jobs (id, title, description, created_at, site) VALUES ('job-1', 'T', '', 'then', 'legacy')`,
		`INSERT INTO worktrees (id, job_id, site, kind, path, branch, base_ref, base_sha, created_at) VALUES ('wt-1', 'job-1', 'legacy', 'integration', '/w', 'b', 'main', 'x', 'then')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close()

	loadMigrationsFn = old
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if list, _ := s.ListSites(); len(list) != 0 {
		t.Fatalf("path-registered Sites still listed: %+v", list)
	}
	if _, err := s.CreateJobOn("legacy", "T2", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("job created on a path-registered Site: %v", err)
	}
	if _, err := s.GetSite("legacy"); err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if j, err := s.GetJob("job-1"); err != nil || j.Site != "legacy" {
		t.Fatalf("job history: %+v %v", j, err)
	}
	if wts, err := s.Worktrees("job-1"); err != nil || len(wts) != 1 {
		t.Fatalf("worktree history: %+v %v", wts, err)
	}
	// Re-registering by URL brings the name back, with its history.
	addSite(t, s, "legacy", "https://github.com/o/legacy")
	if j, err := s.CreateJobOn("legacy", "T3", ""); err != nil || j.Site != "legacy" {
		t.Fatalf("%+v %v", j, err)
	}
}
