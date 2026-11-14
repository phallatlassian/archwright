package runner

import (
	"encoding/json"
)

// codexParser normalizes `codex exec --json` lines into events and
// accumulates the run's result. It is fed one line at a time.
type codexParser struct {
	started     map[string]bool // item ids whose tool_use was already emitted
	denials     []Denial
	lastMessage string // the last agent message
	result      *Result
	failure     string // a turn.failed or error message, if any

	noticeList []string // managed-policy notices seen before anything else
	done       bool     // a line other than a start or a notice was seen
}

// codexDeniedExit is the exit code `codex exec` reports for a command whose
// escalation it cannot approve (docs/research/codex-headless-approvals-test.md).
const codexDeniedExit = -1

func (p *codexParser) settled() bool     { return p.done }
func (p *codexParser) notices() []string { return p.noticeList }

type codexLine struct {
	Type    string    `json:"type"`
	Message string    `json:"message"` // error
	Item    codexItem `json:"item"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"` // turn.failed
}

type codexItem struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Message   string          `json:"message"`
	Command   string          `json:"command"`
	Output    string          `json:"aggregated_output"`
	ExitCode  *int            `json:"exit_code"`
	Status    string          `json:"status"`
	Changes   json.RawMessage `json:"changes"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Err       json.RawMessage `json:"error"`
}

// Tool names the codex adapter reports for the items that are tool uses.
const (
	codexShell      = "shell"
	codexFileChange = "file_change"
)

// feed normalizes one stream line. A line that is not JSON, or has no known
// shape, becomes a KindOther event so the raw line is never lost.
func (p *codexParser) feed(line []byte) []Event {
	raw := json.RawMessage(append([]byte(nil), line...))
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil {
		return []Event{{Kind: KindOther, Text: string(line), Raw: quoted(line)}}
	}
	if l.Type == "item.completed" && l.Item.Type == "error" && !p.done {
		p.noticeList = append(p.noticeList, l.Item.Message)
	} else if l.Type != "thread.started" && l.Type != "turn.started" {
		p.done = true
	}
	switch l.Type {
	case "thread.started":
		return []Event{{Kind: KindStarted, Raw: raw}}
	case "item.started", "item.updated", "item.completed":
		return p.item(l.Type, l.Item, raw)
	case "turn.completed":
		return p.complete(l, raw)
	case "turn.failed":
		p.failure = l.Error.Message
		if p.failure == "" {
			p.failure = "turn failed"
		}
		return []Event{p.fail(raw)}
	case "error":
		// A stream error; the turn.failed that normally follows ends the run.
		if p.failure == "" {
			p.failure = l.Message
		}
		return []Event{{Kind: KindOther, Text: l.Message, IsError: true, Raw: raw}}
	}
	return []Event{{Kind: KindOther, Raw: raw}}
}

func (p *codexParser) item(typ string, it codexItem, raw json.RawMessage) []Event {
	switch it.Type {
	case "agent_message":
		if typ != "item.completed" {
			return []Event{{Kind: KindOther, Raw: raw}}
		}
		p.lastMessage = it.Text
		return []Event{{Kind: KindMessage, Text: it.Text, Raw: raw}}
	case "command_execution":
		return p.command(typ, it, raw)
	case "file_change":
		if typ != "item.completed" {
			return []Event{{Kind: KindOther, Raw: raw}}
		}
		return []Event{
			{Kind: KindToolUse, Tool: codexFileChange, ToolUseID: it.ID, Command: string(it.Changes), Raw: raw},
			{Kind: KindToolResult, ToolUseID: it.ID, IsError: it.Status == "failed", Raw: raw},
		}
	case "mcp_tool_call":
		tool := it.Server + "." + it.Tool
		switch typ {
		case "item.started":
			return []Event{{Kind: KindToolUse, Tool: tool, ToolUseID: it.ID, Command: string(it.Arguments), Raw: raw}}
		case "item.completed":
			text := string(it.Result)
			if len(it.Err) > 0 && string(it.Err) != "null" {
				text = string(it.Err)
			}
			return []Event{{Kind: KindToolResult, ToolUseID: it.ID, Text: text, IsError: it.Status == "failed", Raw: raw}}
		}
	case "error":
		// A non-fatal notice, e.g. what an enterprise-managed requirement changed.
		return []Event{{Kind: KindOther, Text: it.Message, Raw: raw}}
	}
	return []Event{{Kind: KindOther, Raw: raw}}
}

// command maps a shell command item. A command that failed with exit code -1
// is how plain `codex exec` reports an escalation it cannot approve (ADR 0011):
// a permission_denied event follows its result.
func (p *codexParser) command(typ string, it codexItem, raw json.RawMessage) []Event {
	var out []Event
	if typ == "item.started" || !p.started[it.ID] {
		if p.started == nil {
			p.started = map[string]bool{}
		}
		p.started[it.ID] = true
		out = append(out, Event{Kind: KindToolUse, Tool: codexShell, ToolUseID: it.ID, Command: it.Command, Raw: raw})
	}
	if typ != "item.completed" {
		return out
	}
	failed := it.Status == "failed" || (it.ExitCode != nil && *it.ExitCode != 0)
	out = append(out, Event{Kind: KindToolResult, ToolUseID: it.ID, Text: it.Output, IsError: failed, Raw: raw})
	if it.Status == "failed" && it.ExitCode != nil && *it.ExitCode == codexDeniedExit {
		d := Denial{Tool: codexShell, Command: it.Command, ToolUseID: it.ID}
		p.denials = append(p.denials, d)
		out = append(out, Event{Kind: KindPermissionDenied, Tool: d.Tool, Command: d.Command, ToolUseID: d.ToolUseID, Raw: raw})
	}
	return out
}

func (p *codexParser) complete(l codexLine, raw json.RawMessage) []Event {
	p.result = &Result{
		Status:  StatusDone,
		Final:   p.lastMessage,
		Usage:   Usage{InputTokens: l.Usage.InputTokens, OutputTokens: l.Usage.OutputTokens}, // codex reports no cost
		Denials: p.denials,
	}
	return []Event{{Kind: KindResult, Text: p.lastMessage, Raw: raw}}
}

func (p *codexParser) fail(raw json.RawMessage) Event {
	p.result = &Result{Status: StatusFailed, Final: p.lastMessage, Error: p.failure, Denials: p.denials}
	return Event{Kind: KindResult, Text: p.failure, IsError: true, Raw: raw}
}

func (p *codexParser) final() *Result   { return p.result }
func (p *codexParser) denied() []Denial { return p.denials }

// violation: codex reports no posture of its own, so there is nothing to compare.
func (p *codexParser) violation() string { return "" }
