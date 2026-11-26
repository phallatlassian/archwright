package works

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	hosted "github.com/phallatlassian/archwright/internal/session"
)

func TestTranscriptLinesBecomeTimelineEntriesOfTheOldKindsInOrder(t *testing.T) {
	tail := transcriptTail{path: "testdata/claude-transcript.jsonl"}
	got := tail.read(false)
	var kinds, summaries []string
	for _, e := range got {
		kinds = append(kinds, e.kind)
		summaries = append(summaries, e.summary)
		if !json.Valid(e.raw) || !strings.Contains(string(e.raw), `"type"`) {
			t.Errorf("%s has no raw line: %s", e.kind, e.raw)
		}
	}
	wantKinds := []string{"other", "message", "tool_use", "tool_result", "tool_use", "tool_result", "message"}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("kinds = %v\nsummaries = %q", kinds, summaries)
	}
	want := []string{"thinking: I should read the brief first.", "Reading the repo.", "Bash: go test ./...", "ok  \tpkg\t0.1s", "Read: /x/main.go", "error: no such file", "All done."}
	if !slices.Equal(summaries, want) {
		t.Errorf("summaries = %q", summaries)
	}
	if again := tail.read(false); len(again) != 0 {
		t.Errorf("a second read took %d entries again", len(again))
	}
}

func TestAPartialLastLineIsNotTakenUntilItIsComplete(t *testing.T) {
	p := t.TempDir() + "/t.jsonl"
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello there"}]}}`
	if err := os.WriteFile(p, []byte(line+"\n"+line[:40]), 0o600); err != nil {
		t.Fatal(err)
	}
	tail := transcriptTail{path: p}
	if got := tail.read(false); len(got) != 1 {
		t.Fatalf("%d entries, want the one complete line", len(got))
	}
	if got := tail.read(false); len(got) != 0 {
		t.Fatalf("the half-written line was taken: %v", got)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(line[40:] + "\n")
	f.Close()
	got := tail.read(false)
	if len(got) != 1 || got[0].summary != "hello there" {
		t.Fatalf("after completing it: %v", got)
	}
}

func TestAHugeToolResultIsStoredCutAndTheBlobLinesAreDropped(t *testing.T) {
	big := strings.Repeat("x", 200<<10)
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"` + big + `"}]}}`
	got := entriesOf([]byte(line))
	if len(got) != 1 || len(got[0].raw) > rawMax || !strings.Contains(string(got[0].raw), `"truncated":true`) {
		t.Fatalf("entries = %d, raw %d bytes", len(got), len(got[0].raw))
	}
	for _, drop := range []string{`{"type":"file-history-snapshot","snapshot":{}}`, `{"type":"api-request-blob","x":1}`, `not json`} {
		if len(entriesOf([]byte(drop))) != 0 {
			t.Errorf("%s was kept", drop)
		}
	}
}

// transcriptWriter appends lines to a Session's transcript, as its agent does.
type transcriptWriter struct{ path string }

func (w transcriptWriter) says(text string) hosted.Step {
	return hosted.Step{Act: func(context.Context, hosted.LaunchRequest) error {
		f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		b, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}}})
		_, err = f.Write(append(b, '\n'))
		return err
	}}
}

func (w transcriptWriter) start() hosted.Step {
	return hosted.Step{Hook: &hosted.Hook{Event: hosted.HookSessionStart, SessionID: "fake-session", TranscriptPath: w.path}}
}

func (g *sessionRig) messages(run string) []string {
	g.t.Helper()
	var out []string
	for _, e := range mustEvents(g.t, g.st, run) {
		if e.Kind == "message" {
			out = append(out, e.Summary)
		}
	}
	return out
}

func TestTwoRunsOnOneSessionEachGetOnlyTheirOwnTranscriptLines(t *testing.T) {
	w := transcriptWriter{path: t.TempDir() + "/t.jsonl"}
	g := newSessionRig(t, hosted.Script{
		Steps:  []hosted.Step{w.start(), promptHook, w.says("first run says"), reports, stopHook("first run says")},
		Nudged: []hosted.Step{promptHook, w.says("second run says"), reports, stopHook("second run says")},
	})
	g.send("p")
	first := g.waitReported("p")
	waitSessionIdle(t, g.fake.Sessions()[0])
	g.rework("p")
	second := g.waitReported("p")
	g.ended(first.RunID)
	g.ended(second.RunID)
	if got := g.messages(first.RunID); !slices.Equal(got, []string{"first run says"}) {
		t.Errorf("first run messages = %q", got)
	}
	if got := g.messages(second.RunID); !slices.Equal(got, []string{"second run says"}) {
		t.Errorf("second run messages = %q", got)
	}
}

func TestAdoptingASessionAfterARestartTakesNeitherTheEarlierLinesNorLosesTheNewOnes(t *testing.T) {
	w := transcriptWriter{path: t.TempDir() + "/t.jsonl"}
	release := make(chan struct{})
	g := newSessionRig(t, hosted.Script{
		Steps:  []hosted.Step{w.start(), promptHook, w.says("before the restart"), held(release), stopHook("before the restart")},
		Nudged: []hosted.Step{promptHook, w.says("after the restart"), reports, stopHook("after the restart")},
	})
	d := g.workingWright("p")
	eventually(t, "the first run to have it", func() bool { return len(g.messages(d.RunID)) == 1 })
	g.restart()
	close(release)
	next := g.waitReportedRound("p", 1)
	g.ended(next.RunID)
	if got := g.messages(d.RunID); !slices.Equal(got, []string{"before the restart"}) {
		t.Errorf("old run messages = %q", got)
	}
	if got := g.messages(next.RunID); !slices.Equal(got, []string{"after the restart"}) {
		t.Errorf("new run messages = %q", got)
	}
}
