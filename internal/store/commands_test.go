package store

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestDeleteJobRemovesItsRowsAndEventsOnly(t *testing.T) {
	s, _ := open(t)
	keep, _ := s.CreateJob("keep", "")
	gone, _ := s.CreateJob("gone", "")
	if _, err := s.SetPlan(gone.ID, []Step{{ID: "a", Guild: "g", Title: "t"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordCommand("delete_job", gone.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteJob(gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetJob(gone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if evs, _ := s.Events(gone.ID); len(evs) != 0 {
		t.Fatalf("%+v", evs)
	}
	if evs, _ := s.Events(keep.ID); len(evs) != 1 {
		t.Fatalf("another Job's events must stay: %+v", evs)
	}
	tomb, _ := s.EventsAfter(0, "", 100)
	if last := tomb[len(tomb)-1]; last.Type != "job.deleted" || last.JobID != "" {
		t.Fatalf("%+v", last)
	}
	// The append-only guard is closed again once the delete is done.
	if _, err := s.db.Exec("DELETE FROM events WHERE job_id = ?", keep.ID); err == nil {
		t.Fatal("events must stay append-only")
	}
	if err := s.DeleteJob(gone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestEventsAfterIsACursor(t *testing.T) {
	s, _ := open(t)
	for i := 0; i < 5; i++ {
		s.CreateJob("j", "")
	}
	all, _ := s.EventsAfter(0, "", 100)
	page, _ := s.EventsAfter(all[1].ID, "", 2)
	if len(all) != 5 || len(page) != 2 || page[0].ID != all[2].ID || page[1].ID != all[3].ID {
		t.Fatalf("%d %+v", len(all), page)
	}
}

func TestInboxDeliversEachItemOnce(t *testing.T) {
	s, _ := open(t)
	j, _ := s.CreateJob("j", "")
	id, err := s.QueueCommand("instruct", j.ID, "do x", json.RawMessage(`{"text":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReportCommand(id, "pause_job", j.ID, j.ID, "paused", nil); err != nil {
		t.Fatal(err)
	}
	if peek, _ := s.Inbox(true); len(peek) != 2 {
		t.Fatalf("%+v", peek)
	}
	got, _ := s.ReadInbox()
	if len(got) != 2 || got[0].Kind != InboxCommand || got[0].EventID != id || got[1].Kind != InboxReport {
		t.Fatalf("%+v", got)
	}
	if again, _ := s.ReadInbox(); len(again) != 0 {
		t.Fatalf("%+v", again)
	}
	if all, _ := s.Inbox(false); len(all) != 2 || all[0].ReadAt == nil {
		t.Fatalf("%+v", all)
	}
}

func TestPauseAndResumeAreRefusedOutOfState(t *testing.T) {
	s, _ := open(t)
	j, _ := s.CreateJob("j", "")
	if err := s.ResumeJob(j.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("%v", err)
	}
	if err := s.PauseJob(j.ID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.PausedJobs(); len(ids) != 1 {
		t.Fatalf("%v", ids)
	}
	if err := s.PauseJob(j.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("%v", err)
	}
	if err := s.CloseJob(j.ID, "discarded"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeJob(j.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("a closed Job cannot be resumed: %v", err)
	}
	if ids, _ := s.PausedJobs(); len(ids) != 0 {
		t.Fatalf("%v", ids)
	}
}

func TestEveryInboxInsertNotifiesOnceAfterItCommits(t *testing.T) {
	s, _ := open(t)
	j := jobWithPlan(t, s)
	var seen []int
	s.SetInboxNotifier(func() {
		unread, _ := s.Inbox(true) // the item is already committed when the notifier runs
		seen = append(seen, len(unread))
	})
	id, err := s.QueueCommand("cancel_job", j.ID, "cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReportCommand(id, "pause_job", j.ID, j.ID, "paused", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartStep(j.ID, "a", allowStart("masonry", "b")); err != nil {
		t.Fatal(err)
	}
	if err := s.HandBackStep(j.ID, "a", 1, "done", "ok", nil); err != nil {
		t.Fatal(err)
	}
	j2 := jobWithPlan(t, s)
	if _, err := s.StartStep(j2.ID, "a", allowStart("masonry", "b")); err != nil {
		t.Fatal(err)
	}
	if err := s.FailStep(j2.ID, "a", "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.FailStep(j2.ID, "a", "again"); err != nil { // a no-op files nothing, so nudges nothing
		t.Fatal(err)
	}
	if want := []int{1, 2, 3, 4}; !slices.Equal(seen, want) {
		t.Fatalf("unread counts at each notification: %v, want %v", seen, want)
	}
}
