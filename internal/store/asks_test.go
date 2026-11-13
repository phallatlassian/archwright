package store

import (
	"errors"
	"testing"
	"time"
)

var qs = []Question{
	{ID: "a", Text: "Pick", Options: []string{"x", "y"}, Default: "y"},
	{ID: "b", Text: "Many", Options: []string{"p", "q"}, Multiple: true},
	{ID: "c", Text: "Free"},
}

func TestValidateAnswers(t *testing.T) {
	ok := []Answer{{QuestionID: "c", Values: []string{"hi"}}, {QuestionID: "b", Values: []string{"p", "q"}}, {QuestionID: "a", Skipped: true}}
	got, err := ValidateAnswers(qs, ok)
	if err != nil || got[0].QuestionID != "a" || !got[0].Skipped || len(got[1].Values) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	for name, bad := range map[string][]Answer{
		"missing":       ok[:2],
		"duplicate":     append(append([]Answer{}, ok...), ok[0]),
		"not an option": {{QuestionID: "a", Values: []string{"z"}}, ok[1], ok[0]},
		"two for one":   {{QuestionID: "a", Values: []string{"x", "y"}}, ok[1], ok[0]},
		"blank":         {{QuestionID: "a", Values: []string{" "}}, ok[1], ok[0]},
		"empty":         {{QuestionID: "a"}, ok[1], ok[0]},
		"unknown":       append(append([]Answer{}, ok...), Answer{QuestionID: "zz", Skipped: true}),
	} {
		if _, err := ValidateAnswers(qs, bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := DefaultAnswers(qs); err == nil {
		t.Error("defaults accepted though two questions have none")
	}
	if d, err := DefaultAnswers(qs[:1]); err != nil || d[0].Values[0] != "y" {
		t.Errorf("%v %v", d, err)
	}
}

func TestAskLifecycleInTheStore(t *testing.T) {
	s, _ := open(t)
	job, _ := s.CreateJob("J", "")
	a, err := s.CreateAsk(NewAsk{JobID: job.ID, Asker: "warden", Kind: AskDecision, Blocking: true, Questions: qs[:1]})
	if err != nil || a.Holder != HolderArchwright || a.Questions[0].ID != "a" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := s.CreateAsk(NewAsk{JobID: "nope", Asker: "warden", Kind: AskDecision, Questions: qs[:1]}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	// Asks never expire: reading much later changes nothing.
	later := s.now
	s.now = func() time.Time { return later().Add(365 * 24 * time.Hour) }
	if got, _ := s.GetAsk(a.ID); got.Status != AskOpen {
		t.Fatalf("%+v", got)
	}
	if _, err := s.AnswerAsk(a.ID, HolderWarden, "warden", []Answer{{QuestionID: "a", Values: []string{"x"}}}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a Warden answered an Archwright-held ask: %v", err)
	}
	if _, err := s.AnswerAsk(a.ID, HolderArchwright, "dashboard", []Answer{{QuestionID: "a", Values: []string{"x"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerAsk(a.ID, HolderArchwright, "archwright", []Answer{{QuestionID: "a", Values: []string{"y"}}}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("second answer: %v", err)
	}
	if _, err := s.CancelAsk(a.ID, "dashboard"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel after answer: %v", err)
	}
	if got, _ := s.GetAsk(a.ID); got.AnsweredBy != "dashboard" {
		t.Fatalf("%+v", got)
	}
}

func TestInterruptDispatchesLeavesBlockedDispatchAndPendingAsk(t *testing.T) {
	st, _ := open(t)
	job, _ := st.CreateJob("J", "")
	d, _ := st.NewPiece(job.ID, "engineering", "a", []byte(`{}`), "r")
	if err := st.StartDispatch(d.ID); err != nil {
		t.Fatal(err)
	}
	ask, err := st.CreateAsk(NewAsk{JobID: job.ID, Asker: "wright", Guild: "engineering", Piece: "a", DispatchID: d.ID, Kind: "decision", Blocking: true,
		Questions: qs[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if next, err := st.InterruptDispatches(); err != nil || len(next) != 0 {
		t.Fatalf("%v %v", next, err)
	}
	if got, _ := st.GetDispatch(d.ID); got.Status != DispatchBlocked {
		t.Fatalf("blocked dispatch is %s", got.Status)
	}
	if got, _ := st.GetAsk(ask.ID); got.Status != "open" {
		t.Fatalf("ask is %s", got.Status)
	}
}
