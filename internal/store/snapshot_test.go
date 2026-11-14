package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func dirListing(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			out[p] = fi.Size()
		}
		return nil
	})
	return out
}

func TestSnapshotReadsWithoutTouchingTheDatabase(t *testing.T) {
	s, path := open(t) // left open: its write-ahead log is live
	if _, err := s.AddSite(Site{Name: "demo", URL: "https://example.com/demo.git", ClonePath: "/x", BaseBranch: "main", ConcurrencyCap: 2}); err != nil {
		t.Fatal(err)
	}
	before := dirListing(t, filepath.Dir(path))

	snap, err := OpenSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version != SchemaVersion() {
		t.Fatalf("version %d, want %d", snap.Version, SchemaVersion())
	}
	sites, err := snap.Sites()
	if err != nil || len(sites) != 1 || sites[0].Name != "demo" {
		t.Fatalf("sites %v, %v", sites, err)
	}
	if err := snap.Close(); err != nil {
		t.Fatal(err)
	}

	after := dirListing(t, filepath.Dir(path))
	if len(before) != len(after) {
		t.Fatalf("snapshot changed the state directory: %v -> %v", before, after)
	}
	for p, size := range before {
		if after[p] != size {
			t.Fatalf("%s changed size %d -> %d", p, size, after[p])
		}
	}
}

func TestSnapshotOfMissingDatabase(t *testing.T) {
	_, err := OpenSnapshot(filepath.Join(t.TempDir(), "none.db"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want not-exist", err)
	}
}
