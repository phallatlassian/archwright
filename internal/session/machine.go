package session

import (
	"bytes"
	"encoding/json"
)

// Hook event names the state machine reads. Claude sends all of them; Codex
// all but Notification (docs/spec/tmux-runtime.md).
const (
	HookSessionStart     = "SessionStart"
	HookUserPromptSubmit = "UserPromptSubmit"
	HookPreToolUse       = "PreToolUse"
	HookPermissionReq    = "PermissionRequest"
	HookPostToolUse      = "PostToolUse"
	HookNotification     = "Notification"
	HookStop             = "Stop"
	HookSessionEnd       = "SessionEnd"
)

// Hook is the payload a vendor CLI sends a hook, on stdin, as JSON. Fields a
// vendor does not send are empty.
type Hook struct {
	Event          string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	TurnID         string          `json:"turn_id"`
	PermissionMode string          `json:"permission_mode"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
}

// command is the exact command or input of the hook's tool: a shell command
// as the shell sees it, any other tool's input as JSON.
func (h Hook) command() string {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(h.ToolInput, &in) == nil && in.Command != "" {
		return in.Command
	}
	var compact bytes.Buffer
	if len(h.ToolInput) > 0 && json.Compact(&compact, h.ToolInput) == nil && compact.String() != "null" {
		return compact.String()
	}
	return ""
}

// pending is a PermissionRequest the human has not answered yet.
type pending struct{ toolUseID, tool, input string }

// Machine derives a Session's state from its hooks and its pane. It does no
// I/O and is not safe for concurrent use.
//
// Rules, from the spec: a PermissionRequest is outstanding until the matching
// PostToolUse, and a Session is waiting while any is, because Codex fires
// Stop while an approval is pending. SessionStart and UserPromptSubmit are
// deduplicated on kind plus turn id (Codex delivers them twice). Stop is not:
// Codex sends two in one turn, one while the approval is open and one after.
// Notification never changes state.
type Machine struct {
	state       State
	outstanding []pending
	seen        map[string]bool
	info        Info
}

// NewMachine starts in StateStarting.
func NewMachine() *Machine { return &Machine{state: StateStarting, seen: map[string]bool{}} }

// State is the current state.
func (m *Machine) State() State { return m.state }

// Info is what the hooks have reported so far; the pane fields are the caller's.
func (m *Machine) Info() Info { return m.info }

// Change is a state change, with the permission request behind a move to waiting.
type Change struct {
	Prev, State State
	Hook        string
	Tool        string
	Command     string
}

// Apply feeds one hook in and reports the state change it caused, if any.
func (m *Machine) Apply(h Hook) (Change, bool) {
	if m.state == StateEnded {
		return Change{}, false
	}
	if key, ok := dedupeKey(h); ok {
		if m.seen[key] {
			return Change{}, false
		}
		m.seen[key] = true
	}
	switch h.Event {
	case HookSessionStart:
		m.remember(h)
		if m.state == StateStarting { // a later SessionStart (resume, clear) changes nothing
			return m.move(StateIdle, h), true
		}
	case HookUserPromptSubmit:
		m.outstanding = nil // a new turn cannot begin with a prompt still open
		return m.settle(StateWorking, h)
	case HookPreToolUse:
		if len(m.outstanding) > 0 { // a second tool starting does not answer the prompt still open
			return Change{}, false
		}
		return m.settle(StateWorking, h)
	case HookPermissionReq:
		p := pending{toolUseID: h.ToolUseID, tool: h.ToolName, input: h.command()}
		if !m.isOutstanding(p) { // a request delivered twice is still one prompt
			m.outstanding = append(m.outstanding, p)
		}
		return m.move(StateWaitingOnPermission, h), true
	case HookPostToolUse:
		m.clear(h)
		if len(m.outstanding) == 0 && m.state == StateWaitingOnPermission {
			return m.move(StateWorking, h), true
		}
	case HookStop:
		if len(m.outstanding) == 0 && m.state != StateIdle {
			m.info.Turns++
			return m.move(StateIdle, h), true
		}
	case HookSessionEnd:
		return m.End(h.Event), true
	}
	return Change{}, false
}

// End moves to StateEnded: the SessionEnd hook, or the pane's death. It
// reports the change, whatever the state was.
func (m *Machine) End(hook string) Change {
	prev := m.state
	m.state, m.outstanding = StateEnded, nil
	return Change{Prev: prev, State: StateEnded, Hook: hook}
}

// settle moves to a state and reports a change only if it is a different one.
func (m *Machine) settle(to State, h Hook) (Change, bool) {
	c := m.move(to, h)
	return c, c.Prev != c.State
}

func (m *Machine) move(to State, h Hook) Change {
	c := Change{Prev: m.state, State: to, Hook: h.Event}
	if to == StateWaitingOnPermission {
		c.Tool, c.Command = h.ToolName, h.command()
	}
	m.state = to
	return c
}

func (m *Machine) remember(h Hook) {
	if h.SessionID != "" {
		m.info.SessionID = h.SessionID
	}
	if h.TranscriptPath != "" {
		m.info.TranscriptPath = h.TranscriptPath
	}
}

// same says whether two records are one tool use: by tool use id when both
// sides have one, otherwise by tool and input.
func (p pending) same(q pending) bool {
	if p.toolUseID != "" && q.toolUseID != "" {
		return p.toolUseID == q.toolUseID
	}
	return p.tool == q.tool && p.input == q.input
}

func (m *Machine) isOutstanding(p pending) bool {
	for _, o := range m.outstanding {
		if o.same(p) {
			return true
		}
	}
	return false
}

// clear drops the outstanding request a PostToolUse answers.
func (m *Machine) clear(h Hook) {
	done := pending{toolUseID: h.ToolUseID, tool: h.ToolName, input: h.command()}
	for i, p := range m.outstanding {
		if p.same(done) {
			m.outstanding = append(m.outstanding[:i], m.outstanding[i+1:]...)
			return
		}
	}
}

// dedupeKey is what makes a hook the same delivery as an earlier one, for the
// kinds that happen once: SessionStart and SessionEnd per session, and
// UserPromptSubmit per turn id (a Claude one has none, and repeating it changes
// nothing).
func dedupeKey(h Hook) (string, bool) {
	switch h.Event {
	case HookSessionStart, HookSessionEnd:
		return h.Event + "|" + h.SessionID, true
	case HookUserPromptSubmit:
		if h.TurnID != "" {
			return h.Event + "|" + h.SessionID + "|" + h.TurnID, true
		}
	}
	return "", false
}
