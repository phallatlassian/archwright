// Package askpage puts an ask to the developer (ADR 0015): as a Lavish page
// with native controls when `lavish-axi` works, as plain text when it does
// not. The page is only a rendering of the ask record in the Works, which
// stays the source of truth and decides whose answer wins.
package askpage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

// Submission actions a page can send back.
const (
	ActionAnswer        = "answer"
	ActionAcceptDefault = "accept-default"
	ActionSkip          = "skip"
)

// Submission is what the page queues for the Archwright: the `data` of the
// Lavish prompt the developer sent.
type Submission struct {
	AskID   string         `json:"ask_id"`
	Action  string         `json:"action"`
	Answers []store.Answer `json:"answers,omitempty"`
}

// ParseSubmission reads the data of a polled Lavish prompt and checks it is for ask a.
func ParseSubmission(raw []byte, a store.Ask) (Submission, error) {
	var s Submission
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Submission{}, fmt.Errorf("not an ask submission: %w", err)
	}
	if s.AskID != a.ID {
		return Submission{}, fmt.Errorf("submission is for ask %q, not %q", s.AskID, a.ID)
	}
	switch s.Action {
	case ActionAnswer:
		if _, err := store.ValidateAnswers(a.Questions, s.Answers); err != nil {
			return Submission{}, err
		}
	case ActionAcceptDefault, ActionSkip:
	default:
		return Submission{}, fmt.Errorf("unknown action %q", s.Action)
	}
	return s, nil
}

// Tool is the `archwright tool` call that records the submission (Archwright role).
func (s Submission) Tool() (string, json.RawMessage) {
	switch s.Action {
	case ActionAcceptDefault:
		return "accept_default", mustJSON(map[string]string{"ask_id": s.AskID})
	case ActionSkip:
		return "skip_ask", mustJSON(map[string]string{"ask_id": s.AskID})
	}
	return "answer_ask", mustJSON(map[string]any{"ask_id": s.AskID, "answers": s.Answers})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// File is where an ask's page lives: in dir, the neutral factory home, never in a Site.
func File(dir, askID string) string { return filepath.Join(dir, askID+".html") }

// Lavish drives the `lavish-axi` binary.
type Lavish interface {
	Open(file string) (output string, err error)
	End(file string) error
}

// ExecLavish uses `lavish-axi` from PATH; it is nil if there is none.
func ExecLavish() Lavish {
	path, err := exec.LookPath("lavish-axi")
	if err != nil {
		return nil
	}
	return execLavish{path}
}

type execLavish struct{ path string }

func (l execLavish) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, l.path, args...).CombinedOutput()
	return string(out), err
}

func (l execLavish) Open(file string) (string, error) { return l.run(file) }

func (l execLavish) End(file string) error {
	out, err := l.run("end", file)
	if err != nil {
		return fmt.Errorf("lavish-axi end: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// Presentation says how an ask was put to the developer.
type Presentation struct {
	Mode   string `json:"mode"`             // lavish or text
	File   string `json:"file,omitempty"`   // the page, in lavish mode
	Output string `json:"output,omitempty"` // what lavish-axi said when it opened the page
	Text   string `json:"text,omitempty"`   // the plain-text form, in text mode
	Note   string `json:"note,omitempty"`   // why the page was not used
}

// Present writes the ask's page under dir and opens it with Lavish. Without
// Lavish, or if it fails, the same questions come back as plain text.
func Present(a store.Ask, j store.Job, dir string, l Lavish) (Presentation, error) {
	fallback := func(note string) Presentation {
		return Presentation{Mode: "text", Text: Text(a, j), Note: note}
	}
	if l == nil {
		return fallback("lavish-axi is not available; put the questions in the session as text"), nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Presentation{}, err
	}
	file := File(dir, a.ID)
	if err := os.WriteFile(file, []byte(HTML(a, j)), 0o600); err != nil {
		return Presentation{}, err
	}
	out, err := l.Open(file)
	if err != nil {
		return fallback(fmt.Sprintf("lavish-axi failed (%v); put the questions in the session as text", err)), nil
	}
	return Presentation{Mode: "lavish", File: file, Output: strings.TrimSpace(out)}, nil
}

// End ends an ask's page, if it has one open. A missing page or Lavish is not an error.
func End(a store.Ask, dir string, l Lavish) error {
	file := File(dir, a.ID)
	if l == nil {
		return nil
	}
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return l.End(file)
}

// Text renders the ask for a chat session.
func Text(a store.Ask, j store.Job) string {
	var w strings.Builder
	blocking := "not blocking"
	if a.Blocking {
		blocking = "blocking"
	}
	fmt.Fprintf(&w, "Ask %s (%s, %s) from the %s on Job %s %q\n", a.ID, a.Kind, blocking, a.Asker, j.ID, j.Title)
	if a.Why != "" {
		fmt.Fprintf(&w, "Why: %s\n", a.Why)
	}
	for i, q := range a.Questions {
		fmt.Fprintf(&w, "\n%d. [%s] %s\n", i+1, q.ID, q.Text)
		if len(q.Options) > 0 {
			fmt.Fprintf(&w, "   options: %s", strings.Join(q.Options, " | "))
			if q.Multiple {
				w.WriteString(" (pick any)")
			}
			w.WriteString("\n")
		}
		if q.Default != "" {
			fmt.Fprintf(&w, "   recommended default: %s\n", q.Default)
		}
	}
	if a.Kind == store.AskManualStep {
		w.WriteString("\nThis is a checklist: do each step yourself, then confirm it is done. Never paste a secret here.\n")
	} else {
		w.WriteString("\nNever paste a secret here.\n")
	}
	w.WriteString("Reply with an answer per question, \"accept the defaults\", or \"skip\"; the answer is recorded in the Works, where the first answer wins.\n")
	return w.String()
}

var page = template.Must(template.New("ask").Parse(`<!doctype html>
<html lang="en" data-lavish-annotate="off">
<head>
<meta charset="utf-8">
<title>Ask {{.Ask.ID}}</title>
<style>
body{font:16px/1.5 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;color:#1b1f23}
header{border-bottom:1px solid #d0d7de;margin-bottom:1rem}
.meta{color:#57606a;font-size:.9rem}
.q{border:1px solid #d0d7de;border-radius:8px;padding:1rem;margin:1rem 0}
.q h2{font-size:1.05rem;margin:0 0 .5rem}
label{display:block;margin:.25rem 0}
.rec{color:#1a7f37;font-size:.85rem;margin-left:.4rem}
.bar{display:flex;gap:.5rem;flex-wrap:wrap;margin-top:1.5rem}
button{font:inherit;padding:.45rem .9rem;border-radius:6px;border:1px solid #8c959f;background:#f6f8fa;cursor:pointer}
button.primary{background:#1f6feb;color:#fff;border-color:#1f6feb}
.note{background:#fff8c5;border:1px solid #d4a72c;border-radius:6px;padding:.5rem .75rem;font-size:.9rem}
</style>
</head>
<body>
<header>
<h1>{{.Ask.Kind}}{{if .Ask.Blocking}} <small>(blocking)</small>{{end}}</h1>
<p class="meta">Job <b>{{.Job.ID}}</b> {{.Job.Title}}<br>Asked by the {{.Ask.Asker}}{{if .Ask.Guild}} ({{.Ask.Guild}} Guild){{end}}</p>
{{if .Ask.Why}}<p>{{.Ask.Why}}</p>{{end}}
</header>
<p class="note">Never type a secret here. For credentials, do the manual step yourself and tick it off.</p>
<form id="ask" data-lavish-question="{{.Ask.ID}}" data-ask="{{.Ask.ID}}" onsubmit="event.preventDefault(); askSubmit('answer');">
{{range .Questions}}
<section class="q" data-q="{{.ID}}">
<h2>{{.Text}}</h2>
{{if $.Checklist}}
<label><input type="checkbox" name="v-{{.ID}}" value="done"> Done</label>
{{else if .Options}}
{{$q := .}}{{range .Options}}<label><input type="{{if $q.Multiple}}checkbox{{else}}radio{{end}}" name="v-{{$q.ID}}" value="{{.}}"{{if eq . $q.Default}} checked{{end}}> {{.}}{{if eq . $q.Default}}<span class="rec">recommended</span>{{end}}</label>
{{end}}
{{else}}
<label><textarea name="v-{{.ID}}" rows="2" style="width:100%">{{.Default}}</textarea>{{if .Default}}<span class="rec">recommended default pre-filled</span>{{end}}</label>
{{end}}
<label><input type="checkbox" name="skip-{{.ID}}"> Skip this question (the next brief says it is unanswered)</label>
</section>
{{end}}
<div class="bar">
<button type="submit" class="primary">Submit answers</button>
<button type="button"{{if not .AllDefaults}} disabled title="a question has no recommended default"{{end}} onclick="askSubmit('accept-default')">Accept recommended defaults</button>
<button type="button" onclick="askSubmit('skip')">Skip all</button>
</div>
</form>
<script>
function askSubmit(action) {
  const form = document.getElementById('ask');
  const data = { ask_id: form.dataset.ask, action: action };
  let text = action === 'answer' ? 'Answers to ask ' + data.ask_id : (action === 'skip' ? 'Skip ask ' : 'Accept the defaults of ask ') + data.ask_id;
  if (action === 'answer') {
    data.answers = [...form.querySelectorAll('[data-q]')].map((q) => {
      const id = q.dataset.q;
      if (q.querySelector('input[name="skip-' + id + '"]').checked) return { question_id: id, skipped: true };
      const values = [...q.querySelectorAll('[name="v-' + id + '"]')]
        .filter((el) => (el.type === 'checkbox' || el.type === 'radio') ? el.checked : el.value.trim() !== '')
        .map((el) => el.value.trim());
      return { question_id: id, values: values };
    });
  }
  window.lavish.queuePrompt(text, { tag: 'ask-' + action, text: text, element: form, data: data });
  window.lavish.sendQueuedPrompts();
}
</script>
</body>
</html>
`))

// HTML renders the ask as a Lavish page: every question with its options and
// recommended default as native controls, a checklist for manual steps, and
// accept-default and skip controls. It holds nothing that is not in the ask.
func HTML(a store.Ask, j store.Job) string {
	allDefaults := true
	for _, q := range a.Questions {
		allDefaults = allDefaults && q.Default != ""
	}
	var b bytes.Buffer
	err := page.Execute(&b, map[string]any{"Ask": a, "Job": j, "Questions": a.Questions,
		"Checklist": a.Kind == store.AskManualStep, "AllDefaults": allDefaults})
	if err != nil {
		panic(err)
	}
	return b.String()
}
