package launch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// Call makes one tool call to the Works over the run's spool.
type Call func(tool string, input json.RawMessage) (json.RawMessage, error)

// Hook runs one of the Archwright's hooks and writes what the runner expects
// on stdout. A hook never fails the session: when the Works does not answer
// it says so in context and the Archwright falls back to polling.
func Hook(event string, call Call, out io.Writer) error {
	switch event {
	case "session-start":
		return sessionStart(call, out)
	case "prompt":
		text, err := inbox(call)
		if err != nil || text == "" {
			return nil
		}
		return emitContext(out, "UserPromptSubmit", text)
	case "stop":
		text, err := inbox(call)
		if err != nil || text == "" {
			return nil
		}
		return json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": text})
	}
	return fmt.Errorf("unknown hook %q", event)
}

func emitContext(out io.Writer, event, text string) error {
	return json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": text}})
}

// sessionStart rehydrates the session from the store: the Jobs and the unread inbox.
func sessionStart(call Call, out io.Writer) error {
	var b strings.Builder
	b.WriteString(HooksMarker + "\n")
	jobs, err := call("list_jobs", nil)
	if err != nil {
		fmt.Fprintf(&b, "The Works did not answer (%v): poll with `archwright tool list_jobs` and `archwright tool read_inbox`.", err)
		return emitContext(out, "SessionStart", b.String())
	}
	fmt.Fprintf(&b, "Jobs in the store (from list_jobs): %s\n", jobs)
	if text, err := inbox(call); err == nil && text != "" {
		b.WriteString(text)
	}
	return emitContext(out, "SessionStart", b.String())
}

// inbox reads the unread items, each delivered once, and renders them; "" if none.
func inbox(call Call) (string, error) {
	raw, err := call("read_inbox", nil)
	if err != nil {
		return "", err
	}
	var got struct{ Items []store.InboxItem }
	if err := json.Unmarshal(raw, &got); err != nil {
		return "", err
	}
	if len(got.Items) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString(InboxMarker + " Items from the developer's dashboard:\n")
	for _, it := range got.Items {
		job := ""
		if it.JobID != "" {
			job = " (Job " + it.JobID + ")"
		}
		fmt.Fprintf(&b, "- %s: %s%s\n", it.Name, it.Summary, job)
	}
	return b.String(), nil
}
