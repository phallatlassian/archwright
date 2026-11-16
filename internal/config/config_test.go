package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/store"
)

func dirsWith(t *testing.T, file string) paths.Dirs {
	t.Helper()
	d := paths.Dirs{Config: t.TempDir()}
	if file != "" {
		if err := os.WriteFile(File(d), []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestFileIsConfigTomlInTheConfigDir(t *testing.T) {
	if got := File(paths.Dirs{Config: "/c"}); got != filepath.Join("/c", "config.toml") {
		t.Fatal(got)
	}
}

func TestRunnerDefaultsToClaudeAndReadsConfig(t *testing.T) {
	cases := []struct {
		file    string
		want    string
		wantErr bool
	}{
		{"", RunnerClaude, false},
		{"retention_days = 7\n", RunnerClaude, false},
		{"# install\nrunner = \"codex\" # chosen\n", RunnerCodex, false},
		{"runner = 'codex'\n", RunnerCodex, false},
		{"runner = \"grok\"\n", "", true},
	}
	for _, c := range cases {
		got, err := Runner(dirsWith(t, c.file))
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%q: got %q, %v", c.file, got, err)
		}
	}
}

func TestRetention(t *testing.T) {
	cases := []struct {
		file    string
		want    time.Duration
		wantErr bool
	}{
		{"", store.DefaultRetention, false},
		{"runner = \"claude\"\n", store.DefaultRetention, false},
		{"retention_days = 7 # a week\n", 7 * 24 * time.Hour, false},
		{"retention_days = \"90\"\n", 90 * 24 * time.Hour, false},
		{"retention_days = 0\n", 0, true},
		{"retention_days = soon\n", 0, true},
	}
	for _, c := range cases {
		got, err := Retention(dirsWith(t, c.file))
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%q: got %v, %v", c.file, got, err)
		}
	}
}

func TestSettingsAreIndependentAndReadFresh(t *testing.T) {
	d := dirsWith(t, "runner = \"grok\"\nretention_days = 5\n")
	if _, err := Runner(d); err == nil {
		t.Fatal("a bad runner is an error")
	}
	if got, err := Retention(d); err != nil || got != 5*24*time.Hour {
		t.Fatalf("a bad runner must not hide retention: %v, %v", got, err)
	}
	if err := os.WriteFile(File(d), []byte("retention_days = 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Retention(d); err != nil || got != 9*24*time.Hour {
		t.Fatalf("an edit takes effect without a restart: %v, %v", got, err)
	}
}
