package works

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

func ev(typ, job, payload string) store.Event {
	return store.Event{At: time.Date(2026, 11, 21, 4, 21, 46, 0, time.UTC), JobID: job, Type: typ, Payload: json.RawMessage(payload)}
}

func TestFormatEvent(t *testing.T) {
	long := strings.Repeat("é", 300)
	for _, c := range []struct {
		e    store.Event
		want string
	}{
		{ev("step.failed", "job-1", `{"step":"fix","reason":"the Warden ended\nwithout handing back"}`), "2026-11-21T04:21:46Z job-1 step.failed fix: the Warden ended without handing back"},
		{ev("needs_you.review-parked", "job-1", `{"summary":"Hallmark parked the Job after a second failed review"}`), "2026-11-21T04:21:46Z job-1 needs_you.review-parked Hallmark parked the Job after a second failed review"},
		{ev("job.created", "job-1", `{"title":"T","site":"s"}`), "2026-11-21T04:21:46Z job-1 job.created T"},
		{ev("run.started", "job-1", `{"run":"r1","guild":"masonry","runner":"codex"}`), "2026-11-21T04:21:46Z job-1 run.started r1 guild=masonry runner=codex"},
		{ev("run.ended", "job-1", `{"run":"r1","status":"ok"}`), "2026-11-21T04:21:46Z job-1 run.ended r1 ok"},
		{ev("site.registered", "", `{"name":"a","n":3,"nested":{"x":1}}`), "2026-11-21T04:21:46Z - site.registered n=3 name=a"},
		{ev("odd.thing", "j", `not json`), "2026-11-21T04:21:46Z j odd.thing"},
		{ev("job.created", "j", `{"title":"`+long+`"}`), "2026-11-21T04:21:46Z j job.created " + strings.Repeat("é", 200) + "…"},
	} {
		if got := FormatEvent(c.e); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

func readLog(path string) []string {
	b, _ := os.ReadFile(path)
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func startTee(t *testing.T, st *store.Store, path string, max int64) (stop func()) {
	from, _ := st.LastEventID()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); teeEvents(ctx, st, from, path, max, 5*time.Millisecond) }()
	return func() { cancel(); <-done }
}

func TestTeeWritesCommittedEventsFromNowOnceAcrossRestart(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	path := filepath.Join(t.TempDir(), "events.log")
	st.AppendEvent("", "before.start", map[string]string{"a": "b"}) // history stays in sqlite
	stop := startTee(t, st, path, eventLogMax)
	j, _ := st.CreateJob("Fix it", "")
	st.SetJigPlan(j.ID, "fix", []store.Step{{ID: "fix", Guild: "masonry", Title: "do"}})
	st.StartStep(j.ID, "fix", func(store.Plan, []store.StepRun) (string, string, error) { return "masonry", "b", nil })
	st.FailStep(j.ID, "fix", "boom")
	waitFor(t, "step.failed line", func() bool { return strings.Contains(strings.Join(readLog(path), "\n"), "step.failed") })
	stop()

	lines := readLog(path)
	var types []string
	for _, l := range lines {
		f := strings.Fields(l)
		types = append(types, f[2])
	}
	if got := strings.Join(types, ","); got != "job.created,plan.set,step.started,step.failed" {
		t.Fatalf("%s\n%s", got, strings.Join(lines, "\n"))
	}
	if !strings.HasSuffix(lines[3], j.ID+" step.failed fix: boom") && !strings.Contains(lines[3], " "+j.ID+" step.failed fix: boom") {
		t.Fatalf("%q", lines[3])
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// A restart resumes from the newest event: nothing repeats, later events appear.
	st.AppendEvent("", "while.down", map[string]string{})
	stop = startTee(t, st, path, eventLogMax)
	st.AppendEvent("", "after.restart", map[string]string{})
	waitFor(t, "after.restart", func() bool { return strings.Contains(strings.Join(readLog(path), "\n"), "after.restart") })
	stop()
	all := strings.Join(readLog(path), "\n")
	if strings.Contains(all, "before.start") || strings.Contains(all, "while.down") || strings.Count(all, "step.failed") != 1 || len(readLog(path)) != 5 {
		t.Fatal(all)
	}
}

func TestTeeRotatesAndReplacesThePreviousGeneration(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	path := filepath.Join(t.TempDir(), "events.log")
	stop := startTee(t, st, path, 200)
	for i := 0; i < 20; i++ {
		st.AppendEvent("", "tick", map[string]int{"n": i})
		time.Sleep(10 * time.Millisecond)
	}
	waitFor(t, "last line", func() bool { return strings.Contains(strings.Join(readLog(path), "\n"), "n=19") })
	stop()
	old := readLog(path + ".1")
	if len(old) == 0 || len(readLog(path)) == 0 {
		t.Fatal("expected both generations")
	}
	if strings.Contains(strings.Join(old, "\n"), "n=0") {
		t.Fatal("a second rotation should have replaced the first generation")
	}
	if fi, _ := os.Stat(path); fi.Size() > 200 {
		t.Fatalf("over the cap: %d", fi.Size())
	}
}

func TestTeeFailureNeverReachesTheStore(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	os.Mkdir(path, 0o700) // a directory in the way: every write fails
	stop := startTee(t, st, path, eventLogMax)
	if err := st.AppendEvent("", "x", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	stop()
}
