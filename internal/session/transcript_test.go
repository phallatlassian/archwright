package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLastTranscriptMessageIsTheLastAssistantTextOfEitherVendor(t *testing.T) {
	claude := writeTranscript(t,
		`{"type":"user","message":{"role":"user","content":"do it"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I could not finish: tests fail"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}}`,
		`not json`)
	if got := lastTranscriptMessage(claude); got != "I could not finish: tests fail" {
		t.Errorf("claude: %q", got)
	}
	codex := writeTranscript(t,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"do it"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done, almost"}]}}`,
		`{"type":"event_msg","payload":{"type":"token_count"}}`)
	if got := lastTranscriptMessage(codex); got != "done, almost" {
		t.Errorf("codex: %q", got)
	}
	agent := writeTranscript(t, `{"type":"event_msg","payload":{"type":"agent_message","message":"stuck on the build"}}`)
	if got := lastTranscriptMessage(agent); got != "stuck on the build" {
		t.Errorf("agent_message: %q", got)
	}
	if got := lastTranscriptMessage(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("missing transcript: %q", got)
	}
}
