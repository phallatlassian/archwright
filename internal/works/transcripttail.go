package works

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// A Claude Session writes everything its agent does to a transcript, one JSON
// object per line. The run's timeline is read off it as it grows (ADR 0017's
// raw content, kept in the same kinds and shape as a one-shot run's stream).
// Codex's lines differ and are not read yet: its Sessions keep only the
// state-change entries.

const (
	// rawMax is the most of a transcript line that is kept as an entry's raw
	// content. A tool result can be a whole file; the rest is cut and the entry
	// says so.
	rawMax = 32 << 10
	// rawHead is how much of a cut entry's text is kept.
	rawHead = 4 << 10
	// readMax is the most of a transcript one read takes, so a long backlog
	// reaches the timeline in pieces.
	readMax = 4 << 20
)

// transcriptEntry is one timeline entry read off a transcript line.
type transcriptEntry struct {
	kind    string // message, tool_use, tool_result or other
	summary string
	raw     json.RawMessage
}

// transcriptTail is where a run's reading of a Session's transcript is. The
// offset is always at a line start: a line still being written is not taken
// until it is complete.
type transcriptTail struct {
	path string
	off  int64
	// skip makes the next path seen be read from its end, not its start: a
	// resumed Session may continue the file it had, whose lines are not this run's.
	skip bool
}

// follow points the tail at path, the transcript the Session reports now. A new
// file is read from its start, as a SessionStart of a Session that writes a new
// transcript begins one.
func (t *transcriptTail) follow(path string) {
	if path == "" || path == t.path {
		return
	}
	first := t.path == ""
	t.path, t.off = path, 0
	if first && t.skip {
		t.toEnd()
	}
	t.skip = false
}

// toEnd moves the offset to the end of the complete lines now in the file, so a
// run begins with its own lines and none of an earlier run's.
func (t *transcriptTail) toEnd() {
	if t.path == "" {
		return
	}
	for {
		before := t.off
		t.read(true)
		if t.off == before {
			return
		}
	}
}

// read takes the complete lines after the offset and returns what they say. A
// last line that has no newline yet is left, and so is one that is not JSON
// once complete (it is dropped, not retried).
func (t *transcriptTail) read(discard bool) []transcriptEntry {
	if t.path == "" {
		return nil
	}
	f, err := os.Open(t.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() < t.off {
		if err == nil {
			t.off = 0 // the file was replaced by a shorter one: begin it again
		}
		return nil
	}
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, readMax+1))
	if err != nil {
		return nil
	}
	end := bytes.LastIndexByte(b, '\n')
	if end < 0 {
		if len(b) > readMax {
			t.off += int64(len(b)) // a single line longer than a read is dropped
		}
		return nil
	}
	b = b[:end+1]
	t.off += int64(len(b))
	if discard {
		return nil
	}
	var out []transcriptEntry
	for _, line := range bytes.Split(b, []byte("\n")) {
		out = append(out, entriesOf(line)...)
	}
	return out
}

// entriesOf maps one Claude transcript line to timeline entries: each text,
// thinking and tool_use block of an assistant line, and each tool_result of a
// user line. The rest (a person's prompt, snapshots, blobs, queue bookkeeping)
// is dropped: the prompt is already on the timeline as a state or a human note.
func entriesOf(line []byte) []transcriptEntry {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(line, &top) != nil {
		return nil
	}
	var typ string
	_ = json.Unmarshal(top["type"], &typ)
	if typ != "assistant" && typ != "user" {
		return nil
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(top["message"], &msg) != nil {
		return nil
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return nil // a person's prompt is a string
	}
	var out []transcriptEntry
	for _, blk := range blocks {
		var bt string
		_ = json.Unmarshal(blk["type"], &bt)
		var e transcriptEntry
		switch {
		case typ == "assistant" && bt == "text":
			e.kind, e.summary = "message", clipLine(jsonString(blk["text"]))
		case typ == "assistant" && bt == "thinking":
			e.kind, e.summary = "other", "thinking: "+clipLine(jsonString(blk["thinking"]))
		case typ == "assistant" && bt == "tool_use":
			e.kind, e.summary = "tool_use", toolSummary(jsonString(blk["name"]), blk["input"])
		case typ == "user" && bt == "tool_result":
			e.kind, e.summary = "tool_result", clipLine(resultText(blk["content"]))
			if string(blk["is_error"]) == "true" {
				e.summary = "error: " + e.summary
			}
		default:
			continue
		}
		if strings.TrimSpace(e.summary) == "" && e.kind == "message" {
			continue
		}
		e.raw = rawBlock(top, blk)
		out = append(out, e)
	}
	return out
}

// toolSummary is "Name: what it was asked", the command of a shell tool or the
// first line of its input.
func toolSummary(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "description", "prompt"} {
		if s, ok := in[k].(string); ok && s != "" {
			return clipLine(name + ": " + s)
		}
	}
	if len(in) > 0 {
		return clipLine(name + ": " + string(input))
	}
	return clipLine(name)
}

// resultText is a tool result's text: a string, or the text blocks of a list.
func resultText(raw json.RawMessage) string {
	if s := jsonString(raw); s != "" {
		return s
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
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// rawBlock is the line as the old stream carried it, narrowed to one block: a
// line with several blocks gives each entry its own, so no content is stored
// twice. One too large is cut to its head, with its size.
func rawBlock(top map[string]json.RawMessage, blk map[string]json.RawMessage) json.RawMessage {
	var msg map[string]json.RawMessage
	_ = json.Unmarshal(top["message"], &msg)
	one, _ := json.Marshal([]map[string]json.RawMessage{blk})
	msg["content"] = one
	narrow := map[string]json.RawMessage{}
	for _, k := range []string{"type", "uuid", "timestamp", "sessionId"} {
		if v, ok := top[k]; ok {
			narrow[k] = v
		}
	}
	m, _ := json.Marshal(msg)
	narrow["message"] = m
	b, _ := json.Marshal(narrow)
	if len(b) <= rawMax {
		return b
	}
	head := string(b[:rawHead])
	cut, _ := json.Marshal(map[string]any{"type": jsonString(top["type"]), "truncated": true, "bytes": len(b), "head": strings.ToValidUTF8(head, "")})
	return cut
}
