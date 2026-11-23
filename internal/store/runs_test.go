package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRunRecordsKeepSummaryAndPruneRaw(t *testing.T) {
	s, _ := open(t)
	j, _ := s.CreateJob("job", "")
	if err := s.StartRunRecord("run-1", j.ID, "dsp-1", "masonry", "", "", ""); err != nil {
		t.Fatal(err)
	}
	seq, err := s.AddRunEvent("run-1", "tool_use", "Bash: go test", []byte(`{"vendor":"event"}`))
	if err != nil || seq != 1 {
		t.Fatalf("seq %d err %v", seq, err)
	}
	if seq, _ = s.AddRunEvent("run-1", "message", "done", nil); seq != 2 {
		t.Fatalf("seq %d", seq)
	}
	if err := s.SetRunUsage("run-1", "claude", 100, 40, 0.5); err != nil {
		t.Fatal(err)
	}
	if err := s.EndRunRecord("run-1", "done"); err != nil {
		t.Fatal(err)
	}

	evs, _ := s.RunEvents("run-1")
	if len(evs) != 2 || !evs[0].HasRaw || evs[1].HasRaw {
		t.Fatalf("%+v", evs)
	}
	raw, err := s.RunEventRaw("run-1", 1)
	if err != nil || string(raw) != `{"vendor":"event"}` {
		t.Fatalf("%s %v", raw, err)
	}
	use, _ := s.Usage()
	if len(use) != 1 || use[0].Runner != "claude" || use[0].InputTokens != 100 || use[0].CostUSD != 0.5 || use[0].Runs != 1 {
		t.Fatalf("%+v", use)
	}

	if err := s.CloseJob(j.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeJob(j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunEventRaw("run-1", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("raw survived the purge: %v", err)
	}
	evs, _ = s.RunEvents("run-1")
	if len(evs) != 2 || evs[0].Summary != "Bash: go test" || evs[0].HasRaw {
		t.Fatalf("summary lost: %+v", evs)
	}
	if use, _ = s.Usage(); len(use) != 1 {
		t.Fatalf("usage lost: %+v", use)
	}

	if err := s.DeleteJob(j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRunRecord("run-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("run record outlived its Job: %v", err)
	}
}

func TestRunRecordsKeepTheStepAndTheDispatchingWarden(t *testing.T) {
	s, _ := open(t)
	j, _ := s.CreateJob("job", "")
	if err := s.StartRunRecord("w-1", j.ID, "", "masonry", "", "implement", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRunRecord("r-1", j.ID, "dsp-1", "masonry", "", "", "w-1"); err != nil {
		t.Fatal(err)
	}
	recs, err := s.RunRecords(j.ID)
	if err != nil || len(recs) != 2 {
		t.Fatalf("%+v %v", recs, err)
	}
	if recs[0].Step != "implement" || recs[0].ParentRun != "" {
		t.Errorf("the Warden's run: %+v", recs[0])
	}
	if recs[1].Step != "" || recs[1].ParentRun != "w-1" {
		t.Errorf("the Wright's run: %+v", recs[1])
	}
}

func TestRunRecordsFromBeforeTheMigrationHaveNeitherStepNorParent(t *testing.T) {
	s, _ := open(t)
	j, _ := s.CreateJob("job", "")
	if _, err := s.db.Exec("INSERT INTO run_records (run_id, job_id, guild, started_at) VALUES ('old', ?, 'masonry', ?)", j.ID, s.stamp()); err != nil {
		t.Fatal(err)
	}
	rec, err := s.GetRunRecord("old")
	if err != nil || rec.Step != "" || rec.ParentRun != "" {
		t.Fatalf("%+v %v", rec, err)
	}
	b, _ := json.Marshal(rec)
	if strings.Contains(string(b), "step") || strings.Contains(string(b), "parent_run") {
		t.Fatalf("an old run reports a link it does not have: %s", b)
	}
}

func TestADispatchRemembersTheWardenRunThatCreatedItAcrossReworkAndRestart(t *testing.T) {
	s, _ := open(t)
	j, _ := s.CreateJob("job", "")
	d, err := s.NewPiece(j.ID, "masonry", "a", "w-1", []byte(`{}`), "r")
	if err != nil || d.WardenRun != "w-1" {
		t.Fatalf("%+v %v", d, err)
	}
	if got, _ := s.GetDispatch(d.ID); got.WardenRun != "w-1" {
		t.Fatalf("not stored: %+v", got)
	}
	if err := s.StartDispatch(d.ID); err != nil {
		t.Fatal(err)
	}
	next, err := s.InterruptDispatches()
	if err != nil || len(next) != 1 || next[0].WardenRun != "w-1" {
		t.Fatalf("a restart keeps the Warden's run: %+v %v", next, err)
	}
	if err := s.StartDispatch(next[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult(next[0].ID, []byte(`{"status":"done"}`)); err != nil {
		t.Fatal(err)
	}
	_, re, err := s.Rework(j.ID, "a", 2, "why", "", "w-2", []byte(`{}`), "r2")
	if err != nil || re == nil || re.WardenRun != "w-2" {
		t.Fatalf("a rework is the reworking Warden's: %+v %v", re, err)
	}
}

func TestRunBriefIsTheRecordedPromptWithFallbacks(t *testing.T) {
	s, _ := open(t)
	j := jobWithPlan(t, s)
	if _, err := s.StartStep(j.ID, "a", allowStart("masonry", "step brief")); err != nil {
		t.Fatal(err)
	}
	d, err := s.NewPiece(j.ID, "masonry", "p", "w-1", json.RawMessage(`{}`), "rendered brief")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ run, dispatch, step string }{{"w-1", "", "a"}, {"r-1", d.ID, ""}, {"x-1", "", ""}} {
		if err := s.StartRunRecord(c.run, j.ID, c.dispatch, "masonry", "", c.step, ""); err != nil {
			t.Fatal(err)
		}
	}

	// Before the prompt is recorded: the dispatch's or the step's brief, else nothing.
	for run, want := range map[string]RunBrief{
		"w-1": {Text: "step brief", Source: BriefFromStep},
		"r-1": {Text: "rendered brief", Source: BriefFromDispatch},
		"x-1": {Source: BriefNone},
	} {
		if got, err := s.RunBrief(run); err != nil || got != want {
			t.Fatalf("%s: %+v %v, want %+v", run, got, err, want)
		}
	}

	// Recorded: it wins over the fallbacks, and an empty prompt still counts as recorded.
	if err := s.SetRunBrief("r-1", "rendered brief\n\nStarting point"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RunBrief("r-1"); got != (RunBrief{Text: "rendered brief\n\nStarting point", Source: BriefRecorded}) {
		t.Fatalf("%+v", got)
	}
	if err := s.SetRunBrief("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := s.RunBrief("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}

	// The brief is a summary: purging raw run content keeps it.
	for _, run := range []string{"w-1", "r-1", "x-1"} {
		if err := s.EndRunRecord(run, "done"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FailStep(j.ID, "a", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult(d.ID, json.RawMessage(`{"status":"done"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseJob(j.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeJob(j.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RunBrief("r-1"); got.Source != BriefRecorded {
		t.Fatalf("purge dropped the brief: %+v", got)
	}
}
