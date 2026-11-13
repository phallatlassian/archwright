package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestResolve(t *testing.T) {
	cases := []struct {
		name          string
		env           map[string]string
		config, state string
	}{
		{"defaults", map[string]string{"HOME": "/h"}, "/h/.config/archwright", "/h/.local/state/archwright"},
		{"xdg", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x/c", "XDG_STATE_HOME": "/x/s"}, "/x/c/archwright", "/x/s/archwright"},
		{"relative xdg ignored", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "rel"}, "/h/.config/archwright", "/h/.local/state/archwright"},
		{"archwright override wins", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x/c", "ARCHWRIGHT_CONFIG_HOME": "/o/c", "ARCHWRIGHT_STATE_HOME": "/o/s"}, "/o/c", "/o/s"},
	}
	for _, c := range cases {
		d, err := Resolve(env(c.env))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if d.Config != c.config || d.State != c.state {
			t.Errorf("%s: got %+v", c.name, d)
		}
	}
}

func TestResolveNoHome(t *testing.T) {
	if _, err := Resolve(env(nil)); err == nil {
		t.Fatal("expected error without HOME")
	}
}

func TestEnsureIsPrivate(t *testing.T) {
	root := t.TempDir()
	d := Dirs{Config: filepath.Join(root, "c", "archwright"), State: filepath.Join(root, "s", "archwright")}
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{d.Config, d.State} {
		fi, err := os.Stat(dir)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", dir, fi, err)
		}
	}
}

func TestClonesLiveUnderStateOutsideWorktrees(t *testing.T) {
	d := Dirs{Config: "/c", State: "/s"}
	if d.Clones() != "/s/clones" || d.Worktrees() != "/s/worktrees" {
		t.Fatalf("%s %s", d.Clones(), d.Worktrees())
	}
}
