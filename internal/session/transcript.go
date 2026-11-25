package session

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// transcriptTail is how much of the end of a transcript is read to find the
// agent's last message.
const transcriptTail = 256 << 10

// lastTranscriptMessage is the last thing the agent said in the transcript at
// path, or "" when there is none or it cannot be read. It stands in for a Stop
// hook that did not carry the message. Both CLIs write one JSON object per
// line; the line shapes known are Claude's (`message.content[].text` of an
// assistant entry) and Codex's (`payload.content[].text` of an assistant
// message, or an agent_message event). A format change finds nothing, and the
// Session then has no last words, which is all that is lost.
func lastTranscriptMessage(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return ""
	}
	start := max(end-transcriptTail, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	lines := bytes.Split(b, []byte("\n"))
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line is cut
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if t := assistantText(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

type transcriptBody struct {
	Role    string          `json:"role"`
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`
	Content json.RawMessage `json:"content"`
}

// assistantText is the text of an assistant entry of one transcript line.
func assistantText(line []byte) string {
	var top struct {
		Type string `json:"type"`
		transcriptBody
		Payload *transcriptBody `json:"payload"`
	}
	if json.Unmarshal(line, &top) != nil {
		return ""
	}
	if top.Payload != nil && top.Payload.Type == "agent_message" {
		var msg string
		if json.Unmarshal(top.Payload.Message, &msg) == nil {
			return strings.TrimSpace(msg)
		}
	}
	for _, body := range []*transcriptBody{bodyOf(top.Message), top.Payload} {
		if body != nil && body.Role == "assistant" {
			if t := contentText(body.Content); t != "" {
				return t
			}
		}
	}
	return ""
}

func bodyOf(raw json.RawMessage) *transcriptBody {
	var b transcriptBody
	if len(raw) == 0 || json.Unmarshal(raw, &b) != nil {
		return nil
	}
	return &b
}

// contentText joins the text blocks of a message's content, which is a string
// or a list of typed blocks.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if (b.Type == "text" || b.Type == "output_text") && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}
