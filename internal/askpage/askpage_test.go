package askpage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

var job = store.Job{ID: "job-1", Title: "Add <login>"}

var decision = store.Ask{ID: "ask-1", JobID: "job-1", Asker: "wright", Guild: "engineering", Kind: store.AskDecision, Blocking: true, Why: "two schemes fit",
	Questions: []store.Question{
		{ID: "scheme", Text: "Which scheme?", Options: []string{"jwt", "session"}, Default: "session"},
		{ID: "tags", Text: "Which extras?", Options: []string{"a", "b"}, Multiple: true},
		{ID: "note", Text: "Anything to add?"},
	}}

type fakeLavish struct {
	opened, ended []string
	failOpen      error
}

func (f *fakeLavish) Open(file string) (string, error) {
	f.opened = append(f.opened, file)
	return "opened", f.failOpen
}
func (f *fakeLavish) End(file string) error { f.ended = append(f.ended, file); return nil }

func TestPageRendersEveryQuestionWithNativeControls(t *testing.T) {
	h := HTML(decision, job)
	for _, want := range []string{
		`data-lavish-annotate="off"`, `data-ask="ask-1"`,
		`type="radio" name="v-scheme" value="jwt"`, `type="radio" name="v-scheme" value="session" checked`, "recommended",
		`type="checkbox" name="v-tags" value="a"`, `<textarea name="v-note"`,
		`name="skip-scheme"`, "Accept recommended defaults", "Skip all", "window.lavish.queuePrompt",
		"Add &lt;login&gt;", "two schemes fit", "Never type a secret",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if !strings.Contains(h, "disabled") { // note and tags have no default: accepting all defaults is unavailable
		t.Error("accept-defaults should be disabled when a question has no default")
	}
}

func TestManualStepIsAChecklist(t *testing.T) {
	a := store.Ask{ID: "ask-2", JobID: "job-1", Asker: "warden", Kind: store.AskManualStep,
		Questions: []store.Question{{ID: "s1", Text: "Create the API key", Options: []string{"done"}, Default: "done"}}}
	h := HTML(a, job)
	if !strings.Contains(h, `type="checkbox" name="v-s1" value="done"> Done`) || strings.Contains(h, "disabled") {
		t.Fatal(h)
	}
}

func TestPresentOpensALavishPageInTheFactoryHome(t *testing.T) {
	dir := t.TempDir()
	l := &fakeLavish{}
	p, err := Present(decision, job, dir, l)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "ask-1.html")
	if p.Mode != "lavish" || p.File != want || len(l.opened) != 1 || l.opened[0] != want {
		t.Fatalf("%+v %+v", p, l)
	}
	if b, err := os.ReadFile(want); err != nil || !strings.Contains(string(b), "Which scheme?") {
		t.Fatalf("page not written: %v", err)
	}
	if err := End(decision, dir, l); err != nil || len(l.ended) != 1 {
		t.Fatalf("end: %v %+v", err, l)
	}
}

func TestWithoutLavishTheSameQuestionsComeBackAsText(t *testing.T) {
	for name, l := range map[string]Lavish{"missing": nil, "failing": &fakeLavish{failOpen: errors.New("boom")}} {
		p, err := Present(decision, job, t.TempDir(), l)
		if err != nil {
			t.Fatal(err)
		}
		if p.Mode != "text" || p.Note == "" {
			t.Fatalf("%s: %+v", name, p)
		}
		for _, want := range []string{"ask-1", "Which scheme?", "jwt | session", "recommended default: session", "Anything to add?", "Never paste a secret", "two schemes fit"} {
			if !strings.Contains(p.Text, want) {
				t.Errorf("%s: text lacks %q:\n%s", name, want, p.Text)
			}
		}
	}
	if err := End(decision, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestSubmissionsMapToToolCalls(t *testing.T) {
	good := `{"ask_id":"ask-1","action":"answer","answers":[{"question_id":"scheme","values":["jwt"]},{"question_id":"tags","values":["a","b"]},{"question_id":"note","skipped":true}]}`
	s, err := ParseSubmission([]byte(good), decision)
	if err != nil {
		t.Fatal(err)
	}
	name, in := s.Tool()
	var got struct {
		AskID   string         `json:"ask_id"`
		Answers []store.Answer `json:"answers"`
	}
	if name != "answer_ask" || json.Unmarshal(in, &got) != nil || got.AskID != "ask-1" || len(got.Answers) != 3 {
		t.Fatalf("%s %s", name, in)
	}
	for action, tool := range map[string]string{"accept-default": "accept_default", "skip": "skip_ask"} {
		s, err := ParseSubmission([]byte(`{"ask_id":"ask-1","action":"`+action+`"}`), decision)
		if err != nil {
			t.Fatal(err)
		}
		if name, _ := s.Tool(); name != tool {
			t.Errorf("%s -> %s", action, name)
		}
	}
	for name, raw := range map[string]string{
		"other ask":     `{"ask_id":"ask-9","action":"skip"}`,
		"unknown":       `{"ask_id":"ask-1","action":"shout"}`,
		"bad option":    `{"ask_id":"ask-1","action":"answer","answers":[{"question_id":"scheme","values":["ldap"]},{"question_id":"tags","values":["a"]},{"question_id":"note","values":["x"]}]}`,
		"missing":       `{"ask_id":"ask-1","action":"answer","answers":[]}`,
		"extra field":   `{"ask_id":"ask-1","action":"skip","x":1}`,
		"not json":      `nope`,
		"two for one":   `{"ask_id":"ask-1","action":"answer","answers":[{"question_id":"scheme","values":["jwt","session"]},{"question_id":"tags","values":["a"]},{"question_id":"note","values":["x"]}]}`,
		"unknown quest": `{"ask_id":"ask-1","action":"answer","answers":[{"question_id":"zzz","values":["x"]}]}`,
	} {
		if _, err := ParseSubmission([]byte(raw), decision); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
