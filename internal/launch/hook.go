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
	b.WriteString(catalogueContext(call))
	if text, err := inbox(call); err == nil && text != "" {
		b.WriteString(text)
	}
	return emitContext(out, "SessionStart", b.String())
}

// catalogueContext is the trimmed roster: each Guild's name, intake, handback
// and access, and each Jig's name, description and parameters, never the
// charters or the Jig step lists. When the Works cannot supply it, it says how
// to ask.
func catalogueContext(call Call) string {
	raw, err := call("show_catalogue", nil)
	var cat struct {
		Guilds []struct{ Name, Intake, Handback, Access string }
		Jigs   []struct {
			Name, Description string
			Parameters        []struct {
				Name     string
				Required bool
				Default  string
			}
		}
		Errors []struct{ Kind, Name, Error string }
	}
	if err == nil {
		err = json.Unmarshal(raw, &cat)
	}
	if err != nil {
		return fmt.Sprintf("The catalogue is not available (%v): run `archwright tool show_catalogue` before you write a Plan.\n", err)
	}
	one := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	var b strings.Builder
	b.WriteString("Guilds loaded (from show_catalogue); a Plan step's guild must be one of these names, never another:\n")
	for _, g := range cat.Guilds {
		fmt.Fprintf(&b, "- %s (%s). Intake: %s Handback: %s\n", g.Name, g.Access, one(g.Intake), one(g.Handback))
	}
	b.WriteString("Jigs loaded (prebuilt Plans; when one fits, start it with `archwright tool start_jig` {\"job\", \"jig\", \"params\"} rather than hand-building a Plan):\n")
	for _, j := range cat.Jigs {
		var params []string
		for _, p := range j.Parameters {
			switch {
			case p.Required:
				params = append(params, p.Name+" (required)")
			case p.Default != "":
				params = append(params, p.Name+" (default "+p.Default+")")
			default:
				params = append(params, p.Name)
			}
		}
		if len(params) == 0 {
			params = []string{"none"}
		}
		fmt.Fprintf(&b, "- %s: %s Parameters: %s\n", j.Name, one(j.Description), strings.Join(params, ", "))
	}
	for _, e := range cat.Errors {
		fmt.Fprintf(&b, "Definition not loaded: %s %s: %s\n", e.Kind, e.Name, e.Error)
	}
	return b.String()
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
