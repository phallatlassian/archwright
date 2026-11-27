package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scriptedTmux is a tmux that answers list-clients with the activity times in
// the file `clients` (seconds ago, one per line) and logs every other command.
func scriptedTmux(t *testing.T, agesAgo ...int) (tm *Tmux, log func() string) {
	t.Helper()
	dir := t.TempDir()
	var lines []string
	for _, a := range agesAgo {
		lines = append(lines, strconv.FormatInt(time.Now().Unix()-int64(a), 10))
	}
	os.WriteFile(filepath.Join(dir, "clients"), []byte(strings.Join(lines, "\n")), 0o644)
	bin := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do case \"$a\" in list-clients) cat '" + dir + "/clients'; exit 0;; esac; done\n" +
		"echo \"$@\" >> '" + dir + "/log'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Tmux{Tmux: bin, Socket: filepath.Join(dir, "s")}, func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "log"))
		return string(b)
	}
}

func TestFocusMovesOnlyWhenTheClientIsIdle(t *testing.T) {
	cases := []struct {
		name  string
		ages  []int
		focus bool
	}{
		{"no client attached", nil, false},
		{"active client", []int{1}, false},
		{"idle client", []int{60}, true},
		{"one active of two", []int{60, 1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tm, log := scriptedTmux(t, c.ages...)
			got, err := tm.FocusIfIdle("%7", 5*time.Second)
			if err != nil || got != c.focus {
				t.Fatalf("focused = %v, %v; want %v", got, err, c.focus)
			}
			l := log()
			if c.focus {
				if !strings.Contains(l, "select-window -t %7") || !strings.Contains(l, "select-pane -t %7") {
					t.Fatalf("log = %q", l)
				}
			} else if l != "" {
				t.Fatalf("tmux was told %q", l)
			}
			if strings.Contains(l, "send-keys") || strings.Contains(l, "paste-buffer") {
				t.Fatalf("keystrokes sent: %q", l)
			}
		})
	}
}

func TestCodexRunnerFocusesLikeClaude(t *testing.T) {
	tm, log := scriptedTmux(t, 60)
	f, ok := tm.Codex().(Focuser)
	if !ok {
		t.Fatal("codex runner is not a Focuser")
	}
	got, err := f.FocusIfIdle("%7", 5*time.Second)
	if err != nil || !got {
		t.Fatalf("focused = %v, %v", got, err)
	}
	if l := log(); !strings.Contains(l, "select-pane -t %7") || strings.Contains(l, "send-keys") {
		t.Fatalf("log = %q", l)
	}
}
