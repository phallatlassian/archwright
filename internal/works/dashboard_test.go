package works

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

const testToken = "tok-0123456789"

// dash is the HTTP API over a rig, as the dashboard sees it.
type dash struct {
	*rig
	h    *holdRunner
	srv  *httptest.Server
	shut func() // the Works shutting down
}

func newDash(t *testing.T, limits Limits) *dash {
	g, h := newHeldRig(t, limits)
	done := make(chan struct{})
	srv := httptest.NewServer(newHandler(g.st, g.r, nil, testToken, 1, func() {}, done))
	var once sync.Once
	shut := func() { once.Do(func() { close(done) }) }
	t.Cleanup(func() { shut(); srv.Close() })
	return &dash{rig: g, h: h, srv: srv, shut: shut}
}

// do sends one authorised request and returns the status and body.
func (d *dash) do(method, path, body string, hdr map[string]string) (int, string) {
	d.t.Helper()
	req, err := http.NewRequest(method, d.srv.URL+path, strings.NewReader(body))
	if err != nil {
		d.t.Fatal(err)
	}
	if _, set := hdr["Authorization"]; !set {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (d *dash) post(path, body string) string {
	d.t.Helper()
	code, out := d.do("POST", path, body, nil)
	if code >= 300 {
		d.t.Fatalf("POST %s: %d %s", path, code, out)
	}
	return out
}

func (d *dash) events(after int64) []store.Event {
	d.t.Helper()
	code, body := d.do("GET", "/events?after="+strconv.FormatInt(after, 10), "", nil)
	if code != 200 {
		d.t.Fatalf("events: %d %s", code, body)
	}
	var evs []store.Event
	if err := json.Unmarshal([]byte(body), &evs); err != nil {
		d.t.Fatal(err)
	}
	return evs
}

func types(evs []store.Event) []string {
	out := []string{}
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func indexOf(evs []store.Event, typ string) int {
	for i, e := range evs {
		if e.Type == typ {
			return i
		}
	}
	return -1
}

func (d *dash) inbox() []store.InboxItem {
	var out InboxRead
	d.ok(d.arch, "read_inbox", `{}`, &out)
	return out.Items
}

func TestTokenHostAndOriginAreEnforced(t *testing.T) {
	d := newDash(t, Limits{})
	for name, auth := range map[string]string{"missing": "-", "wrong": "Bearer nope", "no scheme": testToken} {
		hdr := map[string]string{"Authorization": auth}
		if auth == "-" {
			hdr["Authorization"] = ""
		}
		if code, _ := d.do("GET", "/jobs", "", hdr); code != http.StatusUnauthorized {
			t.Errorf("%s token on a read: %d", name, code)
		}
		if code, _ := d.do("POST", "/jobs/"+d.job.ID+"/pause", "", hdr); code != http.StatusUnauthorized {
			t.Errorf("%s token on a command: %d", name, code)
		}
	}
	if code, _ := d.do("GET", "/jobs?token="+testToken, "", map[string]string{"Authorization": ""}); code != http.StatusUnauthorized {
		t.Errorf("the query token is only for the event stream: %d", code)
	}
	if got, _ := d.st.GetJob(d.job.ID); got.PausedAt != "" {
		t.Fatal("a refused command must not execute")
	}

	// Cross-origin commands are refused even with the token; same-origin and header-less ones pass.
	for name, hdr := range map[string]map[string]string{
		"foreign origin":   {"Origin": "https://evil.example"},
		"null origin":      {"Origin": "null"},
		"other port":       {"Origin": "http://127.0.0.1:1"},
		"https same host":  {"Origin": strings.Replace(d.srv.URL, "http:", "https:", 1)},
		"fetch cross-site": {"Sec-Fetch-Site": "cross-site"},
		"fetch same-site":  {"Sec-Fetch-Site": "same-site"},
	} {
		if code, _ := d.do("POST", "/jobs/"+d.job.ID+"/pause", "", hdr); code != http.StatusForbidden {
			t.Errorf("%s: %d", name, code)
		}
	}
	if got, _ := d.st.GetJob(d.job.ID); got.PausedAt != "" {
		t.Fatal("a cross-origin command must not execute")
	}
	if evs := d.events(0); indexOf(evs, "command.pause_job") >= 0 {
		t.Fatalf("a refused command is not recorded: %v", types(evs))
	}
	if code, _ := d.do("GET", "/jobs", "", map[string]string{"Origin": "https://evil.example"}); code != 200 {
		t.Errorf("reads are not commands: %d", code)
	}
	if code, body := d.do("POST", "/jobs/"+d.job.ID+"/pause", "", map[string]string{"Origin": d.srv.URL, "Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Errorf("same origin: %d %s", code, body)
	}

	// A rebinding page reaches the loopback socket under its own name.
	req, _ := http.NewRequest("GET", d.srv.URL+"/jobs", nil)
	req.Host = "evil.example"
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host: %d", resp.StatusCode)
	}
}

// sseFrame is one parsed server-sent event.
type sseFrame struct {
	id    int64
	event string
	data  string
}

// sseReader reads frames off an open stream.
type sseReader struct {
	body io.Closer
	sc   *bufio.Scanner
}

func (d *dash) stream(query string, hdr map[string]string) *sseReader {
	d.t.Helper()
	req, _ := http.NewRequest("GET", d.srv.URL+"/events/stream"+query, nil)
	if _, set := hdr["Authorization"]; !set {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body.Close()
		d.t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	d.t.Cleanup(func() { resp.Body.Close() })
	return &sseReader{body: resp.Body, sc: bufio.NewScanner(resp.Body)}
}

// next reads the next event frame, skipping comments and the retry hint.
func (s *sseReader) next(t *testing.T) sseFrame {
	t.Helper()
	var f sseFrame
	got := false
	deadline := time.AfterFunc(5*time.Second, func() { s.body.Close() })
	defer deadline.Stop()
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case line == "":
			if got {
				return f
			}
		case strings.HasPrefix(line, "id: "):
			f.id, _ = strconv.ParseInt(line[4:], 10, 64)
			got = true
		case strings.HasPrefix(line, "event: "):
			f.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			f.data = line[6:]
		}
	}
	t.Fatal("stream ended before the next event")
	return f
}

func TestStreamResumesFromACursorWithoutGaps(t *testing.T) {
	d := newDash(t, Limits{})
	for i := 0; i < 5; i++ {
		if _, err := d.st.CreateJob(fmt.Sprintf("seed %d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	all := d.events(0)
	if len(all) < 6 {
		t.Fatalf("seed events: %v", types(all))
	}

	// Replay from a cursor, then follow live events.
	cursor := all[2].ID
	s := d.stream("?after="+strconv.FormatInt(cursor, 10), nil)
	var seen []int64
	for range all[3:] {
		f := s.next(t)
		seen = append(seen, f.id)
		var e store.Event
		if err := json.Unmarshal([]byte(f.data), &e); err != nil || e.ID != f.id || e.Type != f.event {
			t.Fatalf("frame %+v: %v", f, err)
		}
	}
	d.post("/jobs/"+d.job.ID+"/pause", "")
	live := s.next(t)
	for live.event != "job.paused" {
		seen = append(seen, live.id)
		live = s.next(t)
	}
	seen = append(seen, live.id)
	s.body.Close() // the connection drops

	// Events land while nobody is connected; the reconnect picks up at the last id seen.
	d.post("/jobs/"+d.job.ID+"/resume", "")
	d.post("/jobs/"+d.job.ID+"/pause", "")
	s2 := d.stream("", map[string]string{"Last-Event-ID": strconv.FormatInt(live.id, 10)})
	want := d.events(live.id)
	if len(want) < 3 {
		t.Fatalf("missed events: %v", types(want))
	}
	for _, e := range want {
		f := s2.next(t)
		if f.id != e.ID {
			t.Fatalf("gap or repeat: got %d want %d", f.id, e.ID)
		}
		seen = append(seen, f.id)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] != seen[i-1]+1 && d.eventsBetween(seen[i-1], seen[i]) != 0 {
			t.Fatalf("event skipped between %d and %d", seen[i-1], seen[i])
		}
	}
	if seen[0] != all[3].ID {
		t.Fatalf("replay did not start right after the cursor: %v", seen)
	}

	// Another reader on a single Job sees only that Job's events.
	other, _ := d.st.CreateJob("other", "")
	f := d.stream("?job="+other.ID, nil).next(t)
	if f.event != "job.created" || !strings.Contains(f.data, other.ID) {
		t.Fatalf("filtered stream: %+v", f)
	}
}

// eventsBetween counts events with an id strictly between a and b.
func (d *dash) eventsBetween(a, b int64) int {
	n := 0
	for _, e := range d.events(a) {
		if e.ID < b {
			n++
		}
	}
	return n
}

func TestStreamAcceptsTheTokenAsAQueryAndRejectsBadOnes(t *testing.T) {
	d := newDash(t, Limits{})
	d.stream("?token="+testToken, map[string]string{"Authorization": ""}).next(t)
	if code, _ := d.do("GET", "/events/stream?token=nope", "", map[string]string{"Authorization": ""}); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code, _ := d.do("GET", "/events/stream?after=x", "", nil); code != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d", code)
	}
}

func TestStreamEndsWhenTheWorksShutsDown(t *testing.T) {
	d := newDash(t, Limits{})
	s := d.stream("", nil)
	s.next(t)
	d.shut()
	ended := make(chan struct{})
	go func() {
		for s.sc.Scan() {
		}
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived the Works")
	}
}

func TestCancelJobStopsItsWrightsAndIsRecordedThenReported(t *testing.T) {
	d := newDash(t, Limits{Total: 1, PerSite: 1, PerWarden: 1})
	if got := d.send("a"); got.Status != store.DispatchRunning {
		t.Fatalf("%+v", got)
	}
	if got := d.send("b"); got.Status != store.DispatchQueued {
		t.Fatalf("%+v", got)
	}
	before := d.events(0)
	d.post("/jobs/"+d.job.ID+"/cancel", "")

	for _, piece := range []string{"a", "b"} {
		dis := d.waitReported(piece)
		if dis.Status != store.DispatchReported || !strings.Contains(string(dis.Result), cancelledByDeveloper) || !strings.Contains(string(dis.Result), `"failed"`) {
			t.Fatalf("%s: %s %s", piece, dis.Status, dis.Result)
		}
	}
	if j, _ := d.st.GetJob(d.job.ID); j.Outcome != "discarded" {
		t.Fatalf("outcome %q", j.Outcome)
	}
	if q := d.queue(); len(q.Running)+len(q.Queued) != 0 {
		t.Fatalf("the scheduler still holds work: %+v", q)
	}
	d.fails(d.warden, "dispatch_wright", `{"piece":"c","brief":`+goodBrief+`}`, "conflict")

	evs := d.events(before[len(before)-1].ID)
	cmd, done, rep := indexOf(evs, "command.cancel_job"), indexOf(evs, "job.discarded"), indexOf(evs, "command.executed")
	if cmd != 0 || done < 0 || rep < done {
		t.Fatalf("the command is recorded before it executes and reported after: %v", types(evs))
	}
	items := d.inbox()
	if len(items) != 1 || items[0].Kind != store.InboxReport || items[0].Name != "cancel_job" || items[0].JobID != d.job.ID || items[0].EventID != evs[cmd].ID {
		t.Fatalf("report to the Archwright: %+v", items)
	}
	if again := d.inbox(); len(again) != 0 {
		t.Fatalf("an item is delivered once: %+v", again)
	}
	if code, _ := d.do("POST", "/jobs/"+d.job.ID+"/cancel", "", nil); code != http.StatusConflict {
		t.Fatalf("cancelling twice: %d", code)
	}
	if evs := d.events(0); indexOf(evs[len(evs)-3:], "command.failed") < 0 {
		t.Fatalf("a refused command is recorded as failed: %v", types(evs))
	}
}

func TestCancelOneWright(t *testing.T) {
	d := newDash(t, Limits{Total: 1, PerSite: 1, PerWarden: 1})
	a, b := d.send("a"), d.send("b")
	d.post("/dispatches/"+a.DispatchID+"/cancel", "")
	if dis := d.waitReported("a"); !strings.Contains(string(dis.Result), cancelledByDeveloper) {
		t.Fatalf("%s", dis.Result)
	}
	// Its slot frees, so the queued Wright starts and the Job carries on.
	eventually(t, "b to start", func() bool { return fmt.Sprint(pieces(d.queue().Running)) == "[b]" })
	d.post("/dispatches/"+b.DispatchID+"/cancel", "")
	d.waitReported("b")
	if j, _ := d.st.GetJob(d.job.ID); j.Outcome != "" {
		t.Fatalf("cancelling a Wright leaves the Job open, got %q", j.Outcome)
	}
	if code, _ := d.do("POST", "/dispatches/"+a.DispatchID+"/cancel", "", nil); code != http.StatusConflict {
		t.Fatalf("already finished: %d", code)
	}
	items := d.inbox()
	if len(items) != 2 || items[0].Name != "cancel_wright" {
		t.Fatalf("%+v", items)
	}
}

func TestPauseHoldsQueuedWrightsUntilResume(t *testing.T) {
	d := newDash(t, Limits{Total: 1, PerSite: 1, PerWarden: 1})
	d.send("a")
	if q := d.send("b"); q.Status != store.DispatchQueued {
		t.Fatalf("%+v", q)
	}
	d.post("/jobs/"+d.job.ID+"/pause", "")
	d.h.release("a")
	d.waitReported("a")
	time.Sleep(50 * time.Millisecond)
	if q := d.queue(); len(q.Running) != 0 || len(q.Queued) != 1 || q.Queued[0].Waiting != "the Job is paused" {
		t.Fatalf("a paused Job starts nothing: %+v", q)
	}
	d.fails(d.warden, "dispatch_wright", `{"piece":"c","brief":`+goodBrief+`}`, "conflict")
	if code, _ := d.do("POST", "/jobs/"+d.job.ID+"/pause", "", nil); code != http.StatusConflict {
		t.Fatalf("pausing twice: %d", code)
	}

	d.h.release("b")
	d.post("/jobs/"+d.job.ID+"/resume", "")
	d.waitReported("b")
	if j, _ := d.st.GetJob(d.job.ID); j.PausedAt != "" {
		t.Fatalf("still paused: %+v", j)
	}
	names := []string{}
	for _, it := range d.inbox() {
		names = append(names, it.Name)
	}
	if fmt.Sprint(names) != "[pause_job pause_job resume_job]" && fmt.Sprint(names) != "[pause_job resume_job]" {
		t.Fatalf("reports: %v", names)
	}
}

func TestPlanDecisionsAreCommands(t *testing.T) {
	d := newDash(t, Limits{})
	p, err := d.st.SetPlan(d.job.ID, []store.Step{{ID: "a", Guild: "engineering", Title: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	code, _ := d.do("POST", "/jobs/"+d.job.ID+"/plan/approve", `{}`, nil)
	if code != 400 {
		t.Fatalf("approving needs the reviewed Plan: %d", code)
	}
	body, _ := json.Marshal(map[string]any{"updated_at": p.UpdatedAt})
	d.post("/jobs/"+d.job.ID+"/plan/approve", string(body))
	got, _ := d.st.GetPlan(d.job.ID)
	evs := d.events(0)
	if got.Approval != store.ApprovalApproved || indexOf(evs, "command.approve_plan") > indexOf(evs, "plan.approved") {
		t.Fatalf("%s %v", got.Approval, types(evs))
	}
	if items := d.inbox(); len(items) != 1 || items[0].Name != "approve_plan" || items[0].Kind != store.InboxReport {
		t.Fatalf("%+v", items)
	}
	d.post("/jobs/"+d.job.ID+"/replan", `{"instructions":"split it"}`) // a judgment one alongside
	if code, _ := d.do("POST", "/jobs/"+d.job.ID+"/plan/reject", "", nil); code != http.StatusConflict {
		t.Fatalf("rejecting a decided Plan: %d", code)
	}
}

func TestAskAnswersAreCommands(t *testing.T) {
	g, ask := askRig(t)
	var esc AskReceipt
	g.ok(g.warden, "escalate_ask", `{"ask_id":"`+ask.ID+`","why":"the brief does not say"}`, &esc)
	ask.ID = esc.AskID // the Archwright holds the escalated copy
	srv := httptest.NewServer(newHandler(g.st, g.r, nil, testToken, 1, func() {}, nil))
	defer srv.Close()
	d := &dash{rig: g, srv: srv}
	d.post("/asks/"+ask.ID+"/answer", bothAnswers)
	a, _ := g.st.GetAsk(ask.ID)
	if a.Status != store.AskAnswered || a.AnsweredBy != AnsweredByDashboard {
		t.Fatalf("%+v", a)
	}
	evs := d.events(0)
	if indexOf(evs, "command.answer_ask") < 0 || indexOf(evs, "command.executed") < 0 {
		t.Fatalf("%v", types(evs))
	}
	if code, _ := d.do("POST", "/asks/"+ask.ID+"/skip", "", nil); code != http.StatusConflict {
		t.Fatalf("the first answer wins: %d", code)
	}
	if items := d.inbox(); len(items) != 1 || items[0].Name != "answer_ask" {
		t.Fatalf("%+v", items)
	}
}

func TestJudgmentCommandsLandInTheArchwrightInbox(t *testing.T) {
	d := newDash(t, Limits{})
	for _, c := range []struct{ path, body, name string }{
		{"/jobs/" + d.job.ID + "/instruct", `{"text":"use the new API"}`, "instruct"},
		{"/instruct", `{"text":"what is blocked?"}`, "instruct"},
		{"/jobs/" + d.job.ID + "/retry", `{"instructions":"try the other approach","piece":"a"}`, "retry_job"},
		{"/jobs/" + d.job.ID + "/replan", `{"instructions":"split the work"}`, "replan"},
		{"/jobs/" + d.job.ID + "/change-jig", `{"jig":"fix","note":"it is a bug"}`, "change_jig"},
	} {
		var out struct {
			Queued  bool  `json:"queued"`
			EventID int64 `json:"event_id"`
		}
		code, body := d.do("POST", c.path, c.body, nil)
		if code != http.StatusAccepted || json.Unmarshal([]byte(body), &out) != nil || !out.Queued {
			t.Fatalf("%s: %d %s", c.path, code, body)
		}
		var evs = d.events(out.EventID - 1)
		if evs[0].Type != "command."+c.name || evs[0].ID != out.EventID {
			t.Fatalf("%s is recorded as an event first: %v", c.path, types(evs))
		}
	}
	// Nothing executed: the Job is untouched and no Plan, Wright or review exists.
	if j, _ := d.st.GetJob(d.job.ID); j.Outcome != "" || j.PausedAt != "" {
		t.Fatalf("%+v", j)
	}
	if _, err := d.st.GetPlan(d.job.ID); err == nil {
		t.Fatal("a judgment command must not change the Plan")
	}
	if code, body := d.do("GET", "/inbox?unread=1", "", nil); code != 200 || strings.Count(body, `"kind":"command"`) != 5 {
		t.Fatalf("peek: %d %s", code, body)
	}
	items := d.inbox() // read_inbox delivers them oldest first, each once
	if len(items) != 5 || items[0].Name != "instruct" || items[2].Name != "retry_job" || items[4].Name != "change_jig" || items[0].Kind != store.InboxCommand {
		t.Fatalf("%+v", items)
	}
	if !strings.Contains(string(items[2].Payload), "try the other approach") || items[2].JobID != d.job.ID || items[1].JobID != "" {
		t.Fatalf("%s", items[2].Payload)
	}
	if again := d.inbox(); len(again) != 0 {
		t.Fatalf("%+v", again)
	}
	var peek InboxRead
	d.ok(d.arch, "read_inbox", `{"peek":true}`, &peek)
	if len(peek.Items) != 0 {
		t.Fatal("nothing left unread")
	}

	for name, c := range map[string]struct{ path, body string }{
		"no text":       {"/instruct", `{"text":" "}`},
		"no job":        {"/jobs/nope/instruct", `{"text":"x"}`},
		"no jig":        {"/jobs/" + d.job.ID + "/change-jig", `{}`},
		"unknown field": {"/instruct", `{"text":"x","run":"rm -rf"}`},
		"no retry text": {"/jobs/" + d.job.ID + "/retry", `{}`},
	} {
		if code, _ := d.do("POST", c.path, c.body, nil); code < 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if left := d.inbox(); len(left) != 0 {
		t.Fatalf("refused commands never reach the inbox: %+v", left)
	}
}

func TestOnlyTheArchwrightReadsTheInbox(t *testing.T) {
	d := newDash(t, Limits{})
	d.fails(d.warden, "read_inbox", `{}`, "forbidden")
}

func TestPurgeKeepsTheSummaryAndDeleteRemovesTheJob(t *testing.T) {
	d := newDash(t, Limits{})
	d.send("a")
	if code, _ := d.do("POST", "/jobs/"+d.job.ID+"/purge", "", nil); code != http.StatusConflict {
		t.Fatalf("a Job with a Wright running is refused: %d", code)
	}
	if code, _ := d.do("DELETE", "/jobs/"+d.job.ID, "", nil); code != http.StatusConflict {
		t.Fatalf("a Job with a Wright running is refused: %d", code)
	}
	d.h.release("a")
	d.waitReported("a")

	if dis, _ := d.st.LatestDispatch(d.job.ID, "a"); dis.Rendered == "" {
		t.Fatal("setup: the brief was rendered")
	}
	d.post("/jobs/"+d.job.ID+"/purge", "")
	dis, _ := d.st.LatestDispatch(d.job.ID, "a")
	j, _ := d.st.GetJob(d.job.ID)
	if dis.Rendered != "" || len(dis.Brief) == 0 || len(dis.Result) == 0 || j.PurgedAt == "" {
		t.Fatalf("purge keeps the summary, drops the raw content: %+v %+v", dis, j)
	}
	if code, _ := d.do("POST", "/jobs/"+d.job.ID+"/purge", "", nil); code != http.StatusConflict {
		t.Fatalf("purging twice: %d", code)
	}

	before := d.events(0)
	if code, _ := d.do("DELETE", "/jobs/"+d.job.ID, "", nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if _, err := d.st.GetJob(d.job.ID); err == nil {
		t.Fatal("the Job survived")
	}
	if evs, _ := d.st.Events(d.job.ID); len(evs) != 0 {
		t.Fatalf("its events survived: %v", types(evs))
	}
	tail := d.events(before[len(before)-1].ID)
	if indexOf(tail, "job.deleted") < 0 || indexOf(tail, "command.executed") < 0 {
		t.Fatalf("%v", types(tail))
	}
	if code, _ := d.do("DELETE", "/jobs/"+d.job.ID, "", nil); code != http.StatusNotFound {
		t.Fatalf("deleting twice: %d", code)
	}
}

func TestCommandEventsRecordTheirArguments(t *testing.T) {
	d := newDash(t, Limits{})
	d.post("/jobs/"+d.job.ID+"/instruct", `{"text":"hello"}`)
	evs := d.events(0)
	e := evs[indexOf(evs, "command.instruct")]
	if !bytes.Contains(e.Payload, []byte(`"origin":"dashboard"`)) || !bytes.Contains(e.Payload, []byte("hello")) {
		t.Fatalf("%s", e.Payload)
	}
}

func TestResumeKeepsPausedJobsPausedAcrossARestart(t *testing.T) {
	r, st := newRuns(t)
	j, _ := st.CreateJob("J", "")
	if err := st.PauseJob(j.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Resume(); err != nil {
		t.Fatal(err)
	}
	r.sched.mu.Lock()
	defer r.sched.mu.Unlock()
	if !r.sched.paused[j.ID] {
		t.Fatal("a paused Job must stay paused after the Works restarts")
	}
}
