package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// minTmuxMajor and minTmuxMinor are the oldest tmux the factory runs on: new-window -e and pane
// options (the pane's @run and @role) arrived in 3.0.
const minTmuxMajor, minTmuxMinor = 3, 0

var tmuxVersion = regexp.MustCompile(`(\d+)\.(\d+)`)

const installTmux = "install tmux (macOS: brew install tmux; Debian or Ubuntu: apt install tmux; Fedora: dnf install tmux) so `tmux` is on PATH, then re-run `archwright doctor`"

// checkTmux: tmux is a hard dependency (ADR 0026):
// every agent runs in a pane of a tmux server the Works owns. It is present
// and new enough, and it can start a server on a socket of its own, as the
// Works' does (`-L archwright`): a throwaway one here, torn down again.
func checkTmux(ctx context.Context, r *collector, d Deps) {
	bin, err := d.LookPath("tmux")
	if err != nil {
		r.fail("tmux", "`tmux` is not on PATH, and the factory runs every agent in a tmux pane", installTmux)
		return
	}
	out, err := d.Output(ctx, bin, "-V")
	if err != nil {
		r.fail("tmux", fmt.Sprintf("`%s -V` failed: %s", bin, firstLine(out)), fmt.Sprintf("run `%s -V` yourself and repair the install, or %s", bin, installTmux))
		return
	}
	if m := tmuxVersion.FindStringSubmatch(out); m != nil {
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		if major < minTmuxMajor || (major == minTmuxMajor && minor < minTmuxMinor) {
			r.fail("tmux", fmt.Sprintf("%s (%s) is older than %d.%d, which the factory needs for per-pane environment and options", firstLine(out), bin, minTmuxMajor, minTmuxMinor),
				"upgrade tmux (macOS: brew upgrade tmux; Debian or Ubuntu: apt install --only-upgrade tmux), then re-run `archwright doctor`")
			return
		}
	}
	r.pass("tmux", "%s (%s)", firstLine(out), bin)

	// A short path: a unix socket's is limited to 103 bytes on macOS.
	dir, err := os.MkdirTemp(d.TempDir, "aw-tmux")
	if err != nil {
		r.fail("tmux-server", "cannot make a directory for a throwaway tmux socket: "+err.Error(), "make "+d.TempDir+" writable")
		return
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "t.sock")
	defer d.Output(ctx, bin, "-S", sock, "kill-server")
	if out, err := d.Output(ctx, bin, "-S", sock, "-f", "/dev/null", "new-session", "-d", "-s", "doctor", "-x", "80", "-y", "24"); err != nil {
		r.fail("tmux-server", "tmux could not start a server on a throwaway socket: "+firstLine(out),
			fmt.Sprintf("run `%s -S %s new-session -d` yourself to see why; a sandbox or policy that forbids unix sockets under %s, or a broken install, stops the factory starting its tmux server", bin, sock, d.TempDir))
		return
	}
	r.pass("tmux-server", "a tmux server starts and stops on a socket of its own")
}
