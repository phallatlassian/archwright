package store

import (
	"errors"
	"testing"
)

func TestRunRecordsKeepSummaryAndPruneRaw(t *testing.T) {
	s, _ := open(t)
	j, _ := s.CreateJob("job", "")
	if err := s.StartRunRecord("run-1", j.ID, "dsp-1", "masonry", ""); err != nil {
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
