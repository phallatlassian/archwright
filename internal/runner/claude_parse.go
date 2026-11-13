package runner

import (
	"encoding/json"
	"strings"
)

// claudeParser normalizes `claude -p --output-format stream-json` lines into
// events and accumulates the run's result. It is fed one line at a time.
type claudeParser struct {
	mode    string // the permission mode the session reported at init
	denials []Denial
	seen    map[string]bool // denials already emitted, by tool use id
	result  *Result
	sawInit bool
}

type claudeLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// system/init
	PermissionMode string `json:"permissionMode"`
	// system/permission_denied
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	ToolUseID string          `json:"tool_use_id"`
	// assistant and user
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	// result
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	Usage            struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	PermissionDenials []struct {
		ToolName  string          `json:"tool_name"`
		ToolUseID string          `json:"tool_use_id"`
		ToolInput json.RawMessage `json:"tool_input"`
	} `json:"permission_denials"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// feed normalizes one stream line. A line that is not JSON, or has no known
// shape, becomes a KindOther event so the raw line is never lost.
func (p *claudeParser) feed(line []byte) []Event {
	raw := json.RawMessage(append([]byte(nil), line...))
	var l claudeLine
	if err := json.Unmarshal(line, &l); err != nil {
		return []Event{{Kind: KindOther, Text: string(line), Raw: quoted(line)}}
	}
	switch l.Type {
	case "system":
		switch l.Subtype {
		case "init":
			p.sawInit = true
			p.mode = l.PermissionMode
			return []Event{{Kind: KindStarted, Raw: raw}}
		case "permission_denied":
			return p.denial(l.ToolName, l.ToolUseID, l.ToolInput, raw)
		}
	case "assistant", "user":
		return p.blocks(l, raw)
	case "result":
		return p.finish(l, raw)
	}
	return []Event{{Kind: KindOther, Raw: raw}}
}

func (p *claudeParser) blocks(l claudeLine, raw json.RawMessage) []Event {
	var blocks []claudeBlock
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		var s string // user content may be a bare string
		if json.Unmarshal(l.Message.Content, &s) == nil && s != "" {
			return []Event{{Kind: KindMessage, Text: s, Raw: raw}}
		}
		return []Event{{Kind: KindOther, Raw: raw}}
	}
	var out []Event
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, Event{Kind: KindMessage, Text: b.Text, Raw: raw})
		case "tool_use":
			out = append(out, Event{Kind: KindToolUse, Tool: b.Name, ToolUseID: b.ID, Command: commandOf(b.Input), Raw: raw})
		case "tool_result":
			out = append(out, Event{Kind: KindToolResult, ToolUseID: b.ToolUseID, Text: contentText(b.Content), IsError: b.IsError, Raw: raw})
		}
	}
	if len(out) == 0 {
		return []Event{{Kind: KindOther, Raw: raw}}
	}
	return out
}

func (p *claudeParser) denial(tool, id string, input, raw json.RawMessage) []Event {
	d := Denial{Tool: tool, ToolUseID: id, Command: commandOf(input)}
	key := id
	if key == "" {
		key = tool + "\x00" + d.Command
	}
	if p.seen[key] {
		return nil
	}
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	p.seen[key] = true
	p.denials = append(p.denials, d)
	return []Event{{Kind: KindPermissionDenied, Tool: d.Tool, Command: d.Command, ToolUseID: id, Raw: raw}}
}

func (p *claudeParser) finish(l claudeLine, raw json.RawMessage) []Event {
	var out []Event
	// The final result lists every denial; emit those not already reported.
	for _, d := range l.PermissionDenials {
		out = append(out, p.denial(d.ToolName, d.ToolUseID, d.ToolInput, raw)...)
	}
	res := Result{
		Status:     StatusDone,
		Final:      l.Result,
		Structured: l.StructuredOutput,
		Usage:      Usage{InputTokens: l.Usage.InputTokens, OutputTokens: l.Usage.OutputTokens, CostUSD: l.TotalCostUSD},
		Denials:    p.denials,
	}
	if l.IsError || (l.Subtype != "" && l.Subtype != "success") {
		res.Status = StatusFailed
		res.Error = l.Subtype
		if l.Result != "" {
			res.Error = l.Subtype + ": " + l.Result
		}
	}
	p.result = &res
	return append(out, Event{Kind: KindResult, Text: l.Result, IsError: res.Status == StatusFailed, Raw: raw})
}

// commandOf is the exact command of a tool input: the command of a shell
// tool, otherwise the input as sent.
func commandOf(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) == nil && in.Command != "" {
		return in.Command
	}
	return string(input)
}

// contentText flattens a tool result's content, a string or text blocks.
func contentText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var blocks []claudeBlock
	if json.Unmarshal(c, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(c)
}

// quoted JSON-quotes raw bytes that were not JSON themselves.
func quoted(b []byte) json.RawMessage {
	q, _ := json.Marshal(string(b))
	return q
}
