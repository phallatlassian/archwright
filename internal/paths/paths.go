// Package paths resolves the per-user config and state directories (ADR 0016, 0018).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// Dirs are the per-user directories: Config holds what the developer edits,
// State holds what the Works generates.
type Dirs struct {
	Config string
	State  string
}

// Resolve picks each directory from, in order: ARCHWRIGHT_*_HOME (used as is),
// XDG_*_HOME (plus "archwright"), then the XDG default under the home directory.
func Resolve(getenv func(string) string) (Dirs, error) {
	config, err := resolve(getenv, "ARCHWRIGHT_CONFIG_HOME", "XDG_CONFIG_HOME", ".config")
	if err != nil {
		return Dirs{}, err
	}
	state, err := resolve(getenv, "ARCHWRIGHT_STATE_HOME", "XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return Dirs{}, err
	}
	return Dirs{Config: config, State: state}, nil
}

func resolve(getenv func(string) string, override, xdg, homeRel string) (string, error) {
	if v := getenv(override); v != "" {
		return v, nil
	}
	if v := getenv(xdg); filepath.IsAbs(v) {
		return filepath.Join(v, "archwright"), nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("cannot locate %s: HOME is not set", override)
	}
	return filepath.Join(home, homeRel, "archwright"), nil
}

// Ensure creates both directories, private to the user (mode 0700).
func (d Dirs) Ensure() error {
	for _, dir := range []string{d.Config, d.State} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Database is the path of the Works' SQLite database.
func (d Dirs) Database() string { return filepath.Join(d.State, "archwright.db") }

// Lock is the single-instance lock file.
func (d Dirs) Lock() string { return filepath.Join(d.State, "works.lock") }

// PidFile records the running Works' pid, loopback port and token.
func (d Dirs) PidFile() string { return filepath.Join(d.State, "works.pid") }

// Log is the detached Works' output log.
func (d Dirs) Log() string { return filepath.Join(d.State, "works.log") }

// Worktrees is the directory holding Works-managed git worktrees, outside every Site (ADR 0008).
func (d Dirs) Worktrees() string { return filepath.Join(d.State, "worktrees") }

// Clones is the directory holding the Works' own bare clone of each Site, outside every developer checkout (ADR 0019).
func (d Dirs) Clones() string { return filepath.Join(d.State, "clones") }
