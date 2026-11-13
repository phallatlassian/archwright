package works

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

const blockingAsk = `{"kind":"decision","blocking":true,"why":"two schemes fit","questions":[
{"id":"scheme","text":"Which auth scheme?","options":["jwt","session"],"default":"session"},
{"id":"note","text":"Anything to add?"}]}`

const bothAnswers = `{"answers":[{"question_id":"scheme","values":["jwt"]},{"question_id":"note","values":["use RS256"]}]}`

// askRig dispatches a piece whose Wright blocks on blockingAsk and returns the ask.
func askRig(t *testing.T) (*rig, store.Ask) {
	g := newRig(t, nil)
	g.runner.ScriptAsk("login", blockingAsk)
	g.dispatch("login")
	asks, err := g.st.Asks(store.AskFilter{Job: g.job.ID})
	if err != nil || len(asks) != 1 {
		t.Fatalf("asks %v %v", asks, err)
	}
	return g, asks[0]
}

func (g *rig) waitRound(piece string, round int) store.Dispatch {
	g.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ds, _ := g.st.Dispatches(g.job.ID); len(ds) > round {
			if d := ds[round]; d.Status == store.DispatchReported {
				return d
			}
		}
	}
	g.t.Fatalf("round %d never reported", round)
	return store.Dispatch{}
}

func TestWrightAskParksTheTaskAndRoutesUpTheChain(t *testing.T) {
	g, ask := askRig(t)
	d, _ := g.st.LatestDispatch(g.job.ID, "login")
	if d.Status != store.DispatchBlocked {
		t.Fatalf("dispatch is %s, want blocked", d.Status)
	}
	if ask.Holder != store.HolderWarden || ask.Status != store.AskOpen || !ask.Blocking || ask.Piece != "login" || ask.DispatchID != d.ID {
		t.Fatalf("ask %+v", ask)
	}
	// The piece is parked: the Warden cannot hand back over it.
	g.fails(g.warden, "handback", `{"status":"done"}`, spool.CodeConflict)
	// Neither the Archwright nor the developer sees it until the Warden escalates.
	g.fails(g.arch, "answer_ask", `{"ask_id":"`+ask.ID+`",`+bothAnswers[1:], spool.CodeConflict)
	if items, _ := g.st.NeedsYou(); len(items) != 0 {
		t.Fatalf("needs-you before escalation: %v", items)
	}

	var esc AskReceipt
	g.ok(g.warden, "escalate_ask", `{"ask_id":"`+ask.ID+`","why":"the brief does not say"}`, &esc)
	if esc.Holder != store.HolderArchwright {
		t.Fatalf("escalated ask %+v", esc)
	}
	g.fails(g.warden, "answer_ask", `{"ask_id":"`+ask.ID+`",`+bothAnswers[1:], spool.CodeConflict) // escalated
	g.fails(g.warden, "answer_ask", `{"ask_id":"`+esc.AskID+`",`+bothAnswers[1:], spool.CodeConflict)
	items, _ := g.st.NeedsYou()
	if len(items) != 1 || items[0].Kind != store.NeedsAsk || items[0].JobID != g.job.ID {
		t.Fatalf("needs-you: %+v", items)
	}
	var job JobWithPlan
	g.ok(g.arch, "show_job", `{"id":"`+g.job.ID+`"}`, &job)
	if !job.BlockedOnYou || len(job.Asks) != 2 {
		t.Fatalf("job %+v", job)
	}

	g.ok(g.arch, "answer_ask", `{"ask_id":"`+esc.AskID+`",`+bothAnswers[1:], nil)
	// The answer travelled back down: the Warden's original ask is answered too.
	var back store.Ask
	g.ok(g.warden, "show_ask", `{"ask_id":"`+ask.ID+`"}`, &back)
	if back.Status != store.AskAnswered || back.Answers[0].Values[0] != "jwt" {
		t.Fatalf("original ask %+v", back)
	}
	if items, _ := g.st.NeedsYou(); len(items) != 0 {
		t.Fatalf("needs-you after the answer: %+v", items)
	}

	// A fresh run starts with the original brief plus the Q&A record.
	next := g.waitRound("login", 1)
	if next.Round != 1 || next.ID == d.ID {
		t.Fatalf("resumed dispatch %+v", next)
	}
	for _, want := range []string{"# Brief", "Add login", "Write tests first.", "## Questions and answers", "Which auth scheme?", "A: jwt", "A: use RS256"} {
		if !strings.Contains(next.Rendered, want) {
			t.Errorf("fresh brief lacks %q:\n%s", want, next.Rendered)
		}
	}
	if old, _ := g.st.GetDispatch(d.ID); old.Status != store.DispatchAnswered {
		t.Fatalf("old dispatch is %s", old.Status)
	}
	if p, _ := g.st.GetPiece(g.job.ID, "login"); p.Reworks != 0 {
		t.Fatalf("an answer must not count as a rework: %+v", p)
	}
	// The piece can then be accepted and reworked as usual.
	g.ok(g.warden, "accept_piece", `{"piece":"login"}`, nil)
}

func TestWardenAnswersFromContextWithoutTroublingTheDeveloper(t *testing.T) {
	g, ask := askRig(t)
	g.fails(g.warden, "answer_ask", `{"ask_id":"`+ask.ID+`","answers":[{"question_id":"scheme","values":["ldap"]},{"question_id":"note","values":["x"]}]}`, spool.CodeInvalidInput)
	g.fails(g.warden, "answer_ask", `{"ask_id":"`+ask.ID+`","answers":[{"question_id":"scheme","values":["jwt"]}]}`, spool.CodeInvalidInput) // unanswered question
	g.ok(g.warden, "answer_ask", `{"ask_id":"`+ask.ID+`",`+bothAnswers[1:], nil)
	if items, _ := g.st.NeedsYou(); len(items) != 0 {
		t.Fatalf("developer was troubled: %v", items)
	}
	next := g.waitRound("login", 1)
	if !strings.Contains(next.Rendered, "A: jwt") {
		t.Fatalf("brief: %s", next.Rendered)
	}
}

func TestFirstAnswerWinsAndAsksNeverExpire(t *testing.T) {
	g, ask := askRig(t)
	var esc AskReceipt
	g.ok(g.warden, "escalate_ask", `{"ask_id":"`+ask.ID+`"}`, &esc)
	// Nothing answers it by itself, however long it waits.
	time.Sleep(60 * time.Millisecond)
	if a, _ := g.st.GetAsk(esc.AskID); a.Status != store.AskOpen || len(a.Answers) != 0 {
		t.Fatalf("ask moved on its own: %+v", a)
	}
	// The dashboard answers first; the Archwright's later record is rejected.
	if _, err := g.r.AnswerAsk(esc.AskID, RoleArchwright, AnsweredByDashboard, []store.Answer{{QuestionID: "scheme", Values: []string{"session"}}, {QuestionID: "note", Skipped: true}}); err != nil {
		t.Fatal(err)
	}
	resp := g.call(g.arch, "answer_ask", `{"ask_id":"`+esc.AskID+`",`+bothAnswers[1:])
	if resp.OK || resp.Error.Code != spool.CodeConflict || !strings.Contains(resp.Error.Message, "dashboard") && !strings.Contains(resp.Error.Message, "answered") {
		t.Fatalf("late answer: %+v", resp)
	}
	g.fails(g.arch, "accept_default", `{"ask_id":"`+esc.AskID+`"}`, spool.CodeConflict)
	g.fails(g.arch, "skip_ask", `{"ask_id":"`+esc.AskID+`"}`, spool.CodeConflict)
	g.fails(g.arch, "cancel_ask", `{"ask_id":"`+esc.AskID+`"}`, spool.CodeConflict)
	a, _ := g.st.GetAsk(esc.AskID)
	if a.AnsweredBy != AnsweredByDashboard || a.Answers[0].Values[0] != "session" {
		t.Fatalf("the first answer must stand: %+v", a)
	}
	if ds, _ := g.st.Dispatches(g.job.ID); len(ds) != 2 {
		t.Fatalf("a rejected answer started a run: %d dispatches", len(ds))
	}
}

func TestSkippedQuestionIsMarkedUnansweredInTheNextBrief(t *testing.T) {
	g, ask := askRig(t)
	var esc AskReceipt
	g.ok(g.warden, "escalate_ask", `{"ask_id":"`+ask.ID+`"}`, &esc)
	g.ok(g.arch, "skip_ask", `{"ask_id":"`+esc.AskID+`"}`, nil)
	next := g.waitRound("login", 1)
	if strings.Count(next.Rendered, "(skipped: unanswered)") != 2 {
		t.Fatalf("brief: %s", next.Rendered)
	}
}

func TestAcceptDefaultAnswersWithTheRecommendedDefault(t *testing.T) {
	g, ask := askRig(t)
	var esc AskReceipt
	g.ok(g.warden, "escalate_ask", `{"ask_id":"`+ask.ID+`"}`, &esc)
	// The second question has no recommended default, so the ask cannot be accepted wholesale.
	g.fails(g.arch, "accept_default", `{"ask_id":"`+esc.AskID+`"}`, spool.CodeInvalidInput)
	if a, _ := g.st.GetAsk(esc.AskID); a.Status != store.AskOpen {
		t.Fatalf("ask %+v", a)
	}

	g2 := newRig(t, nil)
	g2.runner.ScriptAsk("p", `{"kind":"decision","blocking":true,"questions":[{"text":"Which?","options":["a","b"],"default":"b"}]}`)
	g2.dispatch("p")
	asks, _ := g2.st.Asks(store.AskFilter{})
	g2.ok(g2.warden, "escalate_ask", `{"ask_id":"`+asks[0].ID+`"}`, &esc)
	g2.ok(g2.arch, "accept_default", `{"ask_id":"`+esc.AskID+`"}`, nil)
	next := g2.waitRound("p", 1)
	if !strings.Contains(next.Rendered, "A: b") {
		t.Fatalf("brief: %s", next.Rendered)
	}
}

func TestCancelDiscardsTheJobAndEndsEveryOpenAsk(t *testing.T) {
	g, ask := askRig(t)
	var esc AskReceipt
	g.ok(g.warden, "escalate_ask", `{"ask_id":"`+ask.ID+`"}`, &esc)
	g.ok(g.arch, "cancel_ask", `{"ask_id":"`+esc.AskID+`"}`, nil)
	job, _ := g.st.GetJob(g.job.ID)
	if job.Outcome != "discarded" {
		t.Fatalf("job %+v", job)
	}
	for _, id := range []string{ask.ID, esc.AskID} {
		if a, _ := g.st.GetAsk(id); a.Status != store.AskCancelled {
			t.Fatalf("ask %s is %s", id, a.Status)
		}
	}
	if d, _ := g.st.LatestDispatch(g.job.ID, "login"); d.Status != store.DispatchRejected {
		t.Fatalf("dispatch is %s", d.Status)
	}
	if items, _ := g.st.NeedsYou(); len(items) != 0 {
		t.Fatalf("needs-you: %+v", items)
	}
	if ds, _ := g.st.Dispatches(g.job.ID); len(ds) != 1 {
		t.Fatalf("a cancelled ask started a run")
	}
}

func TestNonBlockingAskDoesNotParkTheRun(t *testing.T) {
	g := newRig(t, nil)
	d, err := g.st.NewPiece(g.job.ID, "masonry", "p", json.RawMessage(goodBrief), "rendered")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.st.StartDispatch(d.ID); err != nil {
		t.Fatal(err)
	}
	wr, err := g.r.StartFor(RoleWright, t.TempDir(), RunContext{Job: g.job.ID, Guild: "masonry", DispatchID: d.ID})
	if err != nil {
		t.Fatal(err)
	}
	var rec AskReceipt
	g.ok(wr, "ask", `{"kind":"clarification","blocking":false,"questions":[{"text":"Tabs or spaces?"}]}`, &rec)
	if cur, _ := g.st.GetDispatch(d.ID); cur.Status != store.DispatchRunning {
		t.Fatalf("a non-blocking ask parked the run: %s", cur.Status)
	}
	g.ok(g.warden, "answer_ask", `{"ask_id":"`+rec.AskID+`","answers":[{"question_id":"q1","values":["spaces"]}]}`, nil)
	if ds, _ := g.st.Dispatches(g.job.ID); len(ds) != 1 {
		t.Fatalf("an answered non-blocking ask re-dispatched: %d", len(ds))
	}
	// The run reports normally, and a later brief for the piece carries the answer.
	g.ok(wr, "report_result", `{"status":"done","result":"branch x","evidence":["go test"]}`, nil)
	g.ok(g.warden, "rework_wright", `{"piece":"p","reason":"redo","brief":`+goodBrief+`}`, nil)
	ds, _ := g.st.Dispatches(g.job.ID)
	if len(ds) != 2 || !strings.Contains(ds[1].Rendered, "A: spaces") {
		t.Fatalf("rework brief lacks the Q&A record: %+v", ds)
	}
}

func TestAskSchemaAndRoles(t *testing.T) {
	g := newRig(t, nil)
	for name, in := range map[string]string{
		"no kind":          `{"blocking":true,"questions":[{"text":"x"}]}`,
		"bad kind":         `{"kind":"rant","blocking":true,"questions":[{"text":"x"}]}`,
		"no blocking":      `{"kind":"decision","questions":[{"text":"x"}]}`,
		"no questions":     `{"kind":"decision","blocking":false,"questions":[]}`,
		"blank text":       `{"kind":"decision","blocking":false,"questions":[{"text":" "}]}`,
		"default not opt":  `{"kind":"decision","blocking":false,"questions":[{"text":"x","options":["a"],"default":"b"}]}`,
		"duplicate id":     `{"kind":"decision","blocking":false,"questions":[{"id":"a","text":"x"},{"id":"a","text":"y"}]}`,
		"unknown field":    `{"kind":"decision","blocking":false,"extra":1,"questions":[{"text":"x"}]}`,
		"duplicate option": `{"kind":"decision","blocking":false,"questions":[{"text":"x","options":["a","a"]}]}`,
	} {
		if resp := g.call(g.warden, "ask", in); resp.OK || resp.Error.Code != spool.CodeInvalidInput {
			t.Errorf("%s: %+v", name, resp)
		}
	}
	// The Archwright does not raise asks; Wardens cannot accept defaults, skip or cancel.
	g.fails(g.arch, "ask", `{"kind":"decision","blocking":false,"questions":[{"text":"x"}]}`, spool.CodeForbidden)
	for _, tl := range []string{"accept_default", "skip_ask", "cancel_ask"} {
		g.fails(g.warden, tl, `{"ask_id":"x"}`, spool.CodeForbidden)
	}
	g.fails(g.arch, "show_ask", `{"ask_id":"ask-nope"}`, spool.CodeNotFound)
}

func TestWardenAskGoesStraightToTheArchwrightAndSurvivesRestart(t *testing.T) {
	g := newRig(t, nil)
	var rec AskReceipt
	g.ok(g.warden, "ask", `{"kind":"manual-step","blocking":true,"why":"needs a token","questions":[{"id":"s1","text":"Create the API key in the vendor console","options":["done"],"default":"done"}]}`, &rec)
	if rec.Holder != store.HolderArchwright || !rec.Blocking {
		t.Fatalf("receipt %+v", rec)
	}
	for _, want := range []string{"Needs-you inbox", "show_ask", "no fresh run"} {
		if !strings.Contains(rec.Next, want) {
			t.Fatalf("warden receipt next %q lacks %q", rec.Next, want)
		}
	}
	items, _ := g.st.NeedsYou()
	if len(items) != 1 {
		t.Fatalf("needs-you: %+v", items)
	}
	var list []store.Ask
	g.ok(g.arch, "list_asks", `{"status":"open","holder":"archwright"}`, &list)
	if len(list) != 1 || list[0].ID != rec.AskID || list[0].Kind != store.AskManualStep {
		t.Fatalf("list %+v", list)
	}
	g.ok(g.arch, "accept_default", `{"ask_id":"`+rec.AskID+`"}`, nil)
	var got store.Ask
	g.ok(g.warden, "show_ask", `{"ask_id":"`+rec.AskID+`"}`, &got)
	if got.Status != store.AskAnswered {
		t.Fatalf("ask %+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "secret") {
		t.Fatal("unexpected")
	}
}
