package session

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Focuser is a Runner that can bring a Session's pane to the developer when
// they are not typing. It selects the pane and sends it no keystroke: at a
// permission prompt Enter approves it, and the Works never answers a prompt for
// an agent (ADR 0027).
type Focuser interface {
	// FocusIfIdle moves the attached client's window and pane focus to pane,
	// and reports whether it did. It does nothing when no client is attached, or
	// when the most recently active one was active less than idle ago, so the
	// developer's keystrokes cannot land in another agent's prompt.
	FocusIfIdle(pane string, idle time.Duration) (bool, error)
}

// FocusIfIdle implements Focuser: the client's activity time is what tmux
// reports for it (`client_activity`, the last time it received input).
func (t *Tmux) FocusIfIdle(pane string, idle time.Duration) (bool, error) {
	out, err := t.tmux("list-clients", "-t", "="+t.session(), "-F", "#{client_activity}")
	if err != nil {
		return false, err
	}
	var newest int64
	for _, f := range strings.Fields(out) {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return false, fmt.Errorf("tmux client activity %q: %w", f, err)
		}
		newest = max(newest, n)
	}
	if newest == 0 || time.Since(time.Unix(newest, 0)) < idle {
		return false, nil
	}
	if _, err := t.tmux("select-window", "-t", pane); err != nil {
		return false, err
	}
	if _, err := t.tmux("select-pane", "-t", pane); err != nil {
		return false, err
	}
	return true, nil
}
