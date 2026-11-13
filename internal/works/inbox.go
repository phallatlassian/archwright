package works

import (
	"encoding/json"

	"github.com/phallatlassian/archwright/internal/store"
)

// The Archwright's inbox (ADR 0007, 0014): dashboard commands that need its
// judgment, and reports of mechanical ones the Works already executed. It
// reads them through this tool, or a hook that calls it.
var inboxTools = map[string]tool{
	"read_inbox": {roles: []string{RoleArchwright}, call: readInbox},
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
func readInbox(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Peek bool `json:"peek"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	var items []store.InboxItem
	var err error
	if a.Peek {
		items, err = c.st.Inbox(true)
	} else {
		items, err = c.st.ReadInbox()
	}
	if err != nil {
		return nil, err
	}
	return InboxRead{Items: items}, nil
}
