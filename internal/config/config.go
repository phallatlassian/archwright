// Package config owns the per-install configuration file the developer edits,
// config.toml in the config directory (ADR 0016, 0018): where it is, how it is
// read, and the typed settings in it. Each setting is read from the file when
// asked for, so an edit takes effect without a restart, and an invalid value
// in one setting never hides another.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/store"
)

// The runners an install can choose.
const (
	RunnerClaude = "claude"
	RunnerCodex  = "codex"
)

// File is the per-install configuration the developer edits.
func File(d paths.Dirs) string { return filepath.Join(d.Config, "config.toml") }

// Runner returns the install's runner choice: `runner = "claude"` or
// `"codex"`, claude when the file or key is absent.
func Runner(d paths.Dirs) (string, error) {
	raw, ok, err := lookup(d, "runner")
	if err != nil {
		return "", err
	}
	v := unquote(raw)
	if !ok {
		return RunnerClaude, nil
	}
	if v != RunnerClaude && v != RunnerCodex {
		return "", fmt.Errorf("%s: runner %q must be %q or %q", File(d), v, RunnerClaude, RunnerCodex)
	}
	return v, nil
}

// Retention returns how long after a Job ends its raw run content is kept:
// `retention_days = N`, 30 days when the file or key is absent. N must be a
// whole number of days, 1 or more.
func Retention(d paths.Dirs) (time.Duration, error) {
	raw, ok, err := lookup(d, "retention_days")
	if err != nil {
		return 0, err
	}
	if !ok {
		return store.DefaultRetention, nil
	}
	n, err := strconv.Atoi(unquote(raw))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s: retention_days %q must be a whole number of days, 1 or more", File(d), raw)
	}
	return time.Duration(n) * 24 * time.Hour, nil
}

// lookup returns the last value given for key in config.toml, as written, and
// whether the key was there at all; a missing file has no keys.
func lookup(d paths.Dirs, key string) (string, bool, error) {
	f, err := os.Open(File(d))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	var val string
	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		val, found = strings.TrimSpace(v), true
	}
	if err := sc.Err(); err != nil {
		return "", false, err
	}
	return val, found, nil
}

func unquote(v string) string { return strings.Trim(v, `"'`) }
