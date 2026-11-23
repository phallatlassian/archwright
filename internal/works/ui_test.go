package works

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

func TestDashboardPagesAreEmbeddedAndOpenButTheAPIIsNot(t *testing.T) {
	d := newDash(t, Limits{})
	for _, path := range []string{"/", "/ui/app.js", "/ui/style.css"} {
		code, body := d.do("GET", path, "", map[string]string{"Authorization": ""})
		if code != 200 || body == "" {
			t.Errorf("%s without a token: %d", path, code)
		}
	}
	resp, err := http.Get(d.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("page headers: csp %q", csp)
	}
	req, _ := http.NewRequest("GET", d.srv.URL+"/", nil)
	req.Host = "evil.example"
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("rebound host: %v %v", resp, err)
	}
	if code, _ := d.do("GET", "/board", "", map[string]string{"Authorization": ""}); code != http.StatusUnauthorized {
		t.Errorf("the board without a token: %d", code)
	}
	if code, _ := d.do("POST", "/", "", map[string]string{"Authorization": ""}); code != http.StatusUnauthorized {
		t.Errorf("a command to / is not a page: %d", code)
	}
}

func (d *dash) board() map[string]BoardJob {
	d.t.Helper()
	code, body := d.do("GET", "/board", "", nil)
	if code != 200 {
		d.t.Fatalf("board: %d %s", code, body)
	}
	var list []BoardJob
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		d.t.Fatal(err)
	}
	out := map[string]BoardJob{}
	for _, b := range list {
		out[b.ID] = b
	}
	return out
}

func TestBoardPlacesJobsByState(t *testing.T) {
	d := newDash(t, Limits{})
	if got := d.board()[d.job.ID].State; got != BoardPlanning {
		t.Fatalf("no Plan yet: %s", got)
	}
	if _, err := d.st.SetPlan(d.job.ID, []store.Step{{ID: "a", Guild: "masonry", Title: "build"}}); err != nil {
		t.Fatal(err)
	}
	if b := d.board()[d.job.ID]; b.State != BoardNeedsYou || b.Approval != store.ApprovalPending || b.Steps != 1 {
		t.Fatalf("an ad hoc Plan waits on the developer: %+v", b)
	}
	p, _ := d.st.GetPlan(d.job.ID)
	d.post("/jobs/"+d.job.ID+"/plan/approve", `{"updated_at":"`+p.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")+`"}`)
	if got := d.board()[d.job.ID].State; got != BoardIdle {
		t.Fatalf("approved, nothing running: %s", got)
	}
	d.send("p1")
	eventually(t, "the Wright to run", func() bool { return d.board()[d.job.ID].State == BoardRunning })
	d.post("/jobs/"+d.job.ID+"/pause", "")
	if got := d.board()[d.job.ID].State; got != BoardPaused {
		t.Fatalf("paused: %s", got)
	}
	d.h.release("p1")
	d.waitReported("p1")
	if err := d.st.ParkJob(d.job.ID, "partial handback", store.NewNeedsYou{Kind: store.NeedsReviewParked, JobID: d.job.ID, Summary: "parked", DedupeKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if got := d.board()[d.job.ID].State; got != BoardParked {
		t.Fatalf("parked: %s", got)
	}
}

func TestBoardNamesAJobWhoseStepsAllHandedBack(t *testing.T) {
	d := newDash(t, Limits{})
	if _, err := d.st.SetPlan(d.job.ID, []store.Step{{ID: "a", Guild: "masonry", Title: "build"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := d.st.GetPlan(d.job.ID)
	d.post("/jobs/"+d.job.ID+"/plan/approve", `{"updated_at":"`+p.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")+`"}`)
	allow := func(plan store.Plan, runs []store.StepRun) (string, string, error) { return "masonry", "b", nil }
	if _, err := d.st.StartStep(d.job.ID, "a", allow); err != nil {
		t.Fatal(err)
	}
	if err := d.st.FailStep(d.job.ID, "a", "boom"); err != nil {
		t.Fatal(err)
	}
	if got := d.board()[d.job.ID].State; got != BoardIdle {
		t.Fatalf("a failed step is not handed back: %s", got)
	}
	if _, err := d.st.StartStep(d.job.ID, "a", allow); err != nil {
		t.Fatal(err)
	}
	if err := d.st.HandBackStep(d.job.ID, "a", 0, "done", "built", nil); err != nil {
		t.Fatal(err)
	}
	if got := d.board()[d.job.ID].State; got != BoardComplete {
		t.Fatalf("every step handed back, no PR: %s", got)
	}
	if err := d.st.RecordPR(d.job.ID, "https://github.com/o/r/pull/9"); err != nil {
		t.Fatal(err)
	}
	if b := d.board()[d.job.ID]; b.State != BoardPROpen || b.PR != "https://github.com/o/r/pull/9" {
		t.Fatalf("every step handed back, PR recorded: %+v", b)
	}
}

func TestRunDrillDownAndUsageComeFromTheRun(t *testing.T) {
	d := newDash(t, Limits{})
	d.send("p1")
	d.h.release("p1")
	d.waitReported("p1")
	var runs []store.RunRecord
	eventually(t, "the run to end", func() bool {
		_, body := d.do("GET", "/jobs/"+d.job.ID+"/runs", "", nil)
		runs = nil
		json.Unmarshal([]byte(body), &runs)
		return len(runs) == 1 && runs[0].Status == "done"
	})
	run := runs[0]
	if run.Guild == "" || run.Runner != "fake" || run.InputTokens == 0 || run.CostUSD == 0 {
		t.Fatalf("%+v", run)
	}
	var evs []store.RunEvent
	_, body := d.do("GET", "/runs/"+run.RunID+"/events", "", nil)
	json.Unmarshal([]byte(body), &evs)
	if len(evs) < 2 || strings.Contains(body, "vendor") || !evs[0].HasRaw {
		t.Fatalf("the timeline carries no raw content: %s", body)
	}
	code, raw := d.do("GET", "/runs/"+run.RunID+"/events/1/raw", "", nil)
	if code != 200 || !strings.Contains(raw, "runner") {
		t.Fatalf("raw on demand: %d %s", code, raw)
	}
	if code, _ := d.do("GET", "/runs/"+run.RunID+"/events/99/raw", "", nil); code != 404 {
		t.Errorf("a missing event: %d", code)
	}
	if code, _ := d.do("POST", "/runs/"+run.RunID+"/events", "{}", nil); code == 200 {
		t.Error("the drill-down is read-only")
	}
	var rb store.RunBrief
	code, body = d.do("GET", "/runs/"+run.RunID+"/brief", "", nil)
	if code != 200 || json.Unmarshal([]byte(body), &rb) != nil || rb.Source != store.BriefRecorded || rb.Text == "" {
		t.Fatalf("the brief of a run: %d %s", code, body)
	}
	if code, _ := d.do("GET", "/runs/nope/brief", "", nil); code != 404 {
		t.Errorf("a missing run's brief: %d", code)
	}
	var use []store.UsageRow
	_, body = d.do("GET", "/usage", "", nil)
	json.Unmarshal([]byte(body), &use)
	if len(use) != 1 || use[0].Runner != "fake" || use[0].JobID != d.job.ID {
		t.Fatalf("%s", body)
	}
	if b := d.board()[d.job.ID]; b.CostUSD != use[0].CostUSD {
		t.Fatalf("board cost %v", b.CostUSD)
	}
}

func TestDashboardReadsStepStatusFromTheAPIsStepStates(t *testing.T) {
	d := newDash(t, Limits{})
	_, js := d.do("GET", "/ui/app.js", "", map[string]string{"Authorization": ""})
	_, css := d.do("GET", "/ui/style.css", "", map[string]string{"Authorization": ""})
	for _, want := range []string{"job.step_states", "'handed_back'", "failed: 'failed'"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js does not use %s", want)
		}
	}
	if strings.Contains(js[strings.Index(js, "function stepStatuses"):strings.Index(js, "const STATUS_LABEL")], "piece") {
		t.Error("app.js still guesses step status from dispatch pieces")
	}
	for _, want := range []string{".dot.failed", ".tag.failed"} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css has no %s", want)
		}
	}
}

func TestJobDetailListsEveryRunAndServesItsScriptBeforeTheApp(t *testing.T) {
	d := newDash(t, Limits{})
	noAuth := map[string]string{"Authorization": ""}
	_, page := d.do("GET", "/", "", noAuth)
	if i, j := strings.Index(page, "/ui/runs.js"), strings.Index(page, "/ui/app.js"); i < 0 || j < 0 || i > j {
		t.Errorf("runs.js must load before app.js, which starts drawing at once:\n%s", page)
	}
	if !strings.Contains(page, "/ui/runs.css") || strings.Contains(page, "style=") {
		t.Errorf("the page carries inline style or lacks the run list's stylesheet:\n%s", page)
	}
	for _, f := range []string{"/ui/runs.js", "/ui/runs.css"} {
		if code, _ := d.do("GET", f, "", noAuth); code != 200 {
			t.Errorf("%s: %d", f, code)
		}
	}
	_, js := d.do("GET", "/ui/app.js", "", noAuth)
	if strings.Contains(js, "Pieces and Wright runs") || !strings.Contains(js, "runList(") {
		t.Error("the Job detail still has the old Wright-only table")
	}
}

func TestDashboardScriptsMakeNoInlineStyleAttributes(t *testing.T) {
	d := newDash(t, Limits{})
	for _, f := range []string{"/ui/app.js", "/ui/runs.js"} {
		_, js := d.do("GET", f, "", map[string]string{"Authorization": ""})
		for _, banned := range []string{"setAttribute('style'", `setAttribute("style"`, "style=", "innerHTML ="} {
			if strings.Contains(js, banned) {
				t.Errorf("%s uses %s, which the page's CSP or its no-HTML-from-the-API rule forbids", f, banned)
			}
		}
	}
}

// The run list and transcript model is plain JavaScript; its tests run under node, which CI has.
func TestRunListAndTranscriptModel(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/runs_model.test.js", "ui/runs.js").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
