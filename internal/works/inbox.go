package works

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phallatlassian/archwright/internal/launch"
	"github.com/phallatlassian/archwright/internal/store"
)

// The Archwright's inbox (ADR 0007, 0014): dashboard commands that need its
// judgment, and reports of mechanical ones the Works already executed. It
// reads them through this tool, or a hook that calls it. The Works tells an
// idle session to read them by posting a nudge to its inbox socket, which the
// session's hook registers with register_session.
var inboxTools = map[string]tool{
	"read_inbox":       {roles: []string{RoleArchwright}, call: readInbox},
	"register_session": {roles: []string{RoleArchwright}, call: registerSession},
}

func init() {
	for name, t := range inboxTools {
		catalogue[name] = t
	}
}

// InboxRead is the answer to read_inbox.
type InboxRead struct {
	Items []store.InboxItem `json:"items"`
}

// readInbox delivers the unread items, each once, or with peek only shows them.
// max_chars caps the rendered text of what it delivers: the items that do not
// fit stay unread. wait_seconds, when nothing is unread, holds the call open
// for up to that long and returns early on any item, so a session no nudge can
// reach waits for a step's handback here rather than end its turn.
func readInbox(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Peek        bool `json:"peek"`
		MaxChars    int  `json:"max_chars"`
		WaitSeconds int  `json:"wait_seconds"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if a.WaitSeconds < 0 || a.WaitSeconds > maxWaitSeconds {
		return nil, fmt.Errorf("%w: wait_seconds must be between 0 and %d", errInvalidInput, maxWaitSeconds)
	}
	if a.MaxChars < 0 {
		return nil, fmt.Errorf("%w: max_chars must not be negative", errInvalidInput)
	}
	deadline := time.NewTimer(time.Duration(a.WaitSeconds) * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(waitPoll)
	defer tick.Stop()
	for {
		items, err := takeInbox(c.st, a.Peek, a.MaxChars)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 || a.WaitSeconds == 0 {
			c.runs.syncUnread(c.run)
			return InboxRead{Items: items}, nil
		}
		select {
		case <-deadline.C:
			return InboxRead{Items: items}, nil
		case <-c.run.ending:
			return InboxRead{Items: items}, nil
		case <-tick.C:
		}
	}
}

func takeInbox(st *store.Store, peek bool, max int) ([]store.InboxItem, error) {
	if peek {
		return st.Inbox(true)
	}
	size := func(it store.InboxItem) int { return len(launch.InboxLine(it)) }
	items, err := st.ReadInboxUpTo(max, size)
	if err != nil || max <= 0 || len(items) == 0 {
		return items, err
	}
	if over := size(items[0]) - max; over > 0 { // one item alone is larger than the cap
		items[0].Summary = truncate(items[0].Summary, len(items[0].Summary)-over-len("…"))
	}
	return items, nil
}

// truncate cuts s to at most n bytes at a character boundary and marks the cut.
func truncate(s string, n int) string {
	if n < 0 {
		n = 0
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimRight(s[:n], " ") + "…"
}

// registerSession records where the calling Archwright run's session listens,
// so the Works can nudge it (see nudge.go). A session registered with a Works
// that never nudges it simply keeps the hooks and read_inbox's wait.
func registerSession(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Socket string `json:"socket"`
		Token  string `json:"token"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if err := requireField("socket", a.Socket); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(a.Socket) {
		return nil, fmt.Errorf("%w: socket must be an absolute path", errInvalidInput)
	}
	c.run.setSession(a.Socket, a.Token)
	return struct{}{}, nil
}
