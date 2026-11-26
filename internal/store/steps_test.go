package store

import (
	"errors"
	"testing"
)

func allowStart(guild, brief string) func(Plan, []StepRun) (string, string, error) {
	return func(Plan, []StepRun) (string, string, error) { return guild, brief, nil }
}

func jobWithPlan(t *testing.T, s *Store) Job {
	t.Helper()
	j, err := s.CreateJob("J", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetJigPlan(j.ID, "fix", []Step{{ID: "a", Guild: "masonry", Title: "do"}}); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestStartStepRecordsARunningStepOnce(t *testing.T) {
	s, _ := open(t)
	j := jobWithPlan(t, s)
	r, err := s.StartStep(j.ID, "a", allowStart("masonry", "the brief"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StepRunning || r.Brief != "the brief" || r.Guild != "masonry" {
		t.Fatalf("run: %+v", r)
	}
	if _, err := s.StartStep(j.ID, "a", allowStart("masonry", "again")); !errors.Is(err, ErrConflict) {
		t.Fatalf("a running step starts again: %v", err)
	}
	running, err := s.RunningSteps()
	if err != nil || len(running) != 1 || running[0].Step != "a" {
		t.Fatalf("running: %v %v", running, err)
	}
}

func TestStartStepAsksTheCheckAndKeepsNothingWhenItRefuses(t *testing.T) {
	s, _ := open(t)
	j := jobWithPlan(t, s)
	refuse := errors.New("blocked")
	if _, err := s.StartStep(j.ID, "a", func(Plan, []StepRun) (string, string, error) { return "", "", refuse }); !errors.Is(err, refuse) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.GetStepRun(j.ID, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused start left a step: %v", err)
	}
	if _, err := s.StartStep("job-nope", "a", allowStart("g", "b")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no plan: %v", err)
	}
}

func TestHandBackStepEndsTheStepAndFilesTheHandbackInTheInbox(t *testing.T) {
	s, _ := open(t)
	j := jobWithPlan(t, s)
	if _, err := s.StartStep(j.ID, "a", allowStart("masonry", "b")); err != nil {
		t.Fatal(err)
	}
	if err := s.HandBackStep(j.ID, "a", 7, "done", "Step a handed back", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetStepRun(j.ID, "a")
	if err != nil || r.Status != StepHandedBack || r.HandbackID != 7 || r.FinishedAt == nil {
		t.Fatalf("step: %+v %v", r, err)
	}
	items, err := s.Inbox(true)
	if err != nil || len(items) != 1 || items[0].Kind != InboxReport || items[0].Name != "step_handback" || items[0].JobID != j.ID {
		t.Fatalf("inbox: %+v %v", items, err)
	}
	if err := s.HandBackStep(j.ID, "a", 8, "done", "again", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("handing back twice: %v", err)
	}
	if _, err := s.StartStep(j.ID, "a", allowStart("masonry", "b")); !errors.Is(err, ErrConflict) {
		t.Fatalf("a done step starts again: %v", err)
	}
}

func TestFailedStepsCanStartAgain(t *testing.T) {
	s, _ := open(t)
	j := jobWithPlan(t, s)
	if _, err := s.StartStep(j.ID, "a", allowStart("masonry", "one")); err != nil {
		t.Fatal(err)
	}
	if err := s.HandBackStep(j.ID, "a", 1, "failed", "failed", nil); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.GetStepRun(j.ID, "a"); r.Status != StepFailed {
		t.Fatalf("status %s", r.Status)
	}
	r, err := s.StartStep(j.ID, "a", allowStart("masonry", "two"))
	if err != nil || r.Status != StepRunning || r.Brief != "two" || r.HandbackID != 0 || r.FinishedAt != nil {
		t.Fatalf("restart: %+v %v", r, err)
	}
	if err := s.FailStep(j.ID, "a", "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.FailStep(j.ID, "a", "again"); err != nil { // already failed: no-op
		t.Fatal(err)
	}
	if r, _ := s.GetStepRun(j.ID, "a"); r.Status != StepFailed || r.Reason != "boom" {
		t.Fatalf("step: %+v", r)
	}
}

func TestStepsAreDeletedWithTheirJobAndCountAsActive(t *testing.T) {
	s, _ := open(t)
	j := jobWithPlan(t, s)
	if _, err := s.StartStep(j.ID, "a", allowStart("masonry", "b")); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ActiveRuns(); err != nil || n != 1 {
		t.Fatalf("active runs %d %v", n, err)
	}
	if err := s.PurgeJob(j.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("purging a Job with a running step: %v", err)
	}
	if err := s.FailStep(j.ID, "a", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteJob(j.ID); err != nil {
		t.Fatal(err)
	}
	if runs, _ := s.StepRuns(j.ID); len(runs) != 0 {
		t.Fatalf("steps left: %v", runs)
	}
}

func TestAWardensBlockingAskParksItsStepAndTheAnswerResumesIt(t *testing.T) {
	s, _ := open(t)
	j := jobWithPlan(t, s)
	if _, err := s.StartStep(j.ID, "a", allowStart("masonry", "the brief")); err != nil {
		t.Fatal(err)
	}
	q := []Question{{ID: "q", Text: "Accept?"}}
	// A non-blocking ask parks nothing; a blocking one parks the running step.
	if _, err := s.CreateAsk(NewAsk{JobID: j.ID, Asker: "warden", Guild: "masonry", Step: "a", Kind: AskDecision, Questions: q}); err != nil {
		t.Fatal(err)
	}
	if sr, _ := s.GetStepRun(j.ID, "a"); sr.Status != StepRunning {
		t.Fatalf("a non-blocking ask parked the step: %s", sr.Status)
	}
	ask, err := s.CreateAsk(NewAsk{JobID: j.ID, Asker: "warden", Guild: "masonry", Step: "a", Kind: AskDecision, Blocking: true, Questions: q})
	if err != nil {
		t.Fatal(err)
	}
	if sr, _ := s.GetStepRun(j.ID, "a"); sr.Status != StepBlocked {
		t.Fatalf("step is %s, want blocked", sr.Status)
	}
	if err := s.FailStep(j.ID, "a", "its run ended"); err != nil {
		t.Fatal(err)
	}
	if sr, _ := s.GetStepRun(j.ID, "a"); sr.Status != StepBlocked {
		t.Fatalf("the end of the Warden's run failed a parked step: %s", sr.Status)
	}
	if _, err := s.StartStep(j.ID, "a", allowStart("masonry", "again")); !errors.Is(err, ErrConflict) {
		t.Fatalf("a parked step was started again: %v", err)
	}
	res, err := s.AnswerAsk(ask.ID, HolderArchwright, "dashboard", []Answer{{QuestionID: "q", Values: []string{"yes"}}}, nil)
	if err != nil || len(res.Steps) != 1 || res.Steps[0].Status != StepRunning || res.Steps[0].Step != "a" {
		t.Fatalf("answer: %+v %v", res, err)
	}
	if sr, _ := s.GetStepRun(j.ID, "a"); sr.Status != StepRunning {
		t.Fatalf("step is %s after the answer", sr.Status)
	}
	if as, _ := s.StepAsks(j.ID, "a"); len(as) != 2 {
		t.Fatalf("step asks %+v", as)
	}
}
