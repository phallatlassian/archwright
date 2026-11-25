package works

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/forge"
)

var (
	passingChecks = []forge.Check{{Name: "build", Status: "completed", Conclusion: "success"}, {Name: "lint", Status: "completed", Conclusion: "skipped"}}
	failingChecks = []forge.Check{{Name: "build", Status: "completed", Conclusion: "success"}, {Name: "test", Status: "completed", Conclusion: "failure"}, {Name: "slow", Status: "in_progress"}}
)

func TestCISummaryFoldsTheChecksIntoOneWord(t *testing.T) {
	for name, c := range map[string]struct {
		checks []forge.Check
		want   string
	}{
		"none":            {nil, "none"},
		"all passed":      {passingChecks, "passing"},
		"one failed":      {failingChecks, "failing"},
		"one running":     {[]forge.Check{passingChecks[0], {Name: "x", Status: "queued"}}, "pending"},
		"cancelled":       {[]forge.Check{{Status: "completed", Conclusion: "cancelled"}}, "failing"},
		"neutral is fine": {[]forge.Check{{Status: "completed", Conclusion: "neutral"}}, "passing"},
	} {
		if got := ciSummary(c.checks); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

// A recorded PR is read by the Works itself, at once and in the background,
// and the reading carries no title or body.
func TestRecordingAPRMakesTheWorksReadItsStateAndCI(t *testing.T) {
	g, f, env := recordRig(t)
	f.set(forge.PullRequest{Number: 12, State: "OPEN", Mergeable: "MERGEABLE", Checks: failingChecks}, nil)
	g.ok(env, "record_pr", recordArgs, nil)
	eventually(t, "the Works to read the PR", func() bool { _, ok := g.r.PRStatus(g.job.ID); return ok })
	s, _ := g.r.PRStatus(g.job.ID)
	if s.URL != sitePR || s.Number != 12 || s.State != "OPEN" || s.CI != "failing" || s.Mergeable != "MERGEABLE" || len(s.Checks) != 3 || s.Error != "" || s.CheckedAt == "" {
		t.Fatalf("%+v", s)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "title") || strings.Contains(string(raw), "body") {
		t.Fatalf("third-party text leaked into the reading: %s", raw)
	}
	if f.reads[0] != sitePR {
		t.Fatalf("the Forge was asked for %q", f.reads[0])
	}
}

func TestThePassReadsOnlyJobsWithAnOpenPRAndOnlyWhenTheReadingIsOld(t *testing.T) {
	g, f, _ := recordRig(t)
	f.set(forge.PullRequest{Number: 12, State: "OPEN", Checks: passingChecks}, nil)
	now := time.Now()

	g.r.refreshPRs(now)
	if f.readCount() != 0 {
		t.Fatal("a Job with no PR was read")
	}
	if err := g.st.RecordPR(g.job.ID, sitePR); err != nil {
		t.Fatal(err)
	}
	g.r.refreshPRs(now)
	g.r.refreshPRs(now.Add(time.Second))
	if f.readCount() != 1 {
		t.Fatalf("reads: %d, want 1 (a fresh reading is not read again)", f.readCount())
	}
	g.r.refreshPRs(now.Add(prReadAge + time.Second))
	if f.readCount() != 2 {
		t.Fatalf("reads: %d, want 2 once the reading is old", f.readCount())
	}
	if s, _ := g.r.PRStatus(g.job.ID); s.CI != "passing" {
		t.Fatalf("%+v", s)
	}

	// A Job the developer closed is no longer read.
	if err := g.st.CloseJob(g.job.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	g.r.refreshPRs(now.Add(10 * prReadAge))
	if f.readCount() != 2 {
		t.Fatalf("a closed Job was read: %d", f.readCount())
	}
}

func TestAMergedOrClosedPRIsReadNoMore(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		g, f, _ := recordRig(t)
		f.set(forge.PullRequest{Number: 12, State: state}, nil)
		if err := g.st.RecordPR(g.job.ID, sitePR); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		g.r.refreshPRs(now)
		g.r.refreshPRs(now.Add(10 * prReadAge))
		if f.readCount() != 1 {
			t.Errorf("%s: reads %d, want 1", state, f.readCount())
		}
	}
}

// A failed read is shown, not hidden or raised: the last good reading stays,
// nothing is filed for the developer and the next pass tries again.
func TestAFailedReadKeepsTheLastReadingAndRaisesNothing(t *testing.T) {
	g, f, _ := recordRig(t)
	if err := g.st.RecordPR(g.job.ID, sitePR); err != nil {
		t.Fatal(err)
	}
	f.set(forge.PullRequest{Number: 12, State: "OPEN", Checks: passingChecks}, nil)
	now := time.Now()
	g.r.refreshPRs(now)
	f.set(forge.PullRequest{}, &forge.Error{Kind: forge.KindAuth, Detail: "gh auth login"})
	g.r.refreshPRs(now.Add(prReadAge + time.Second))
	s, _ := g.r.PRStatus(g.job.ID)
	if s.State != "OPEN" || s.CI != "passing" || !strings.Contains(s.Error, "gh auth login") {
		t.Fatalf("%+v", s)
	}
	if items, _ := g.st.NeedsYou(); len(items) != 0 {
		t.Fatalf("a Needs-you item was filed: %+v", items)
	}
	f.set(forge.PullRequest{Number: 12, State: "OPEN", Checks: failingChecks}, nil)
	g.r.refreshPRs(now.Add(2*prReadAge + 2*time.Second))
	if s, _ := g.r.PRStatus(g.job.ID); s.Error != "" || s.CI != "failing" {
		t.Fatalf("%+v", s)
	}
}

func TestAHostWithNoAdapterIsReportedUnreadable(t *testing.T) {
	g, f, _ := recordRig(t)
	if err := g.st.RecordPR(g.job.ID, sitePR); err != nil {
		t.Fatal(err)
	}
	f.set(forge.PullRequest{}, forge.ErrUnsupported)
	g.r.refreshPRs(time.Now())
	if s, ok := g.r.PRStatus(g.job.ID); !ok || !strings.Contains(s.Error, "unsupported") {
		t.Fatalf("%+v", s)
	}
}

// The dashboard reads it from the board and from the Job.
func TestTheBoardAndTheJobCarryTheWorksOwnReadingOfThePR(t *testing.T) {
	d := newDash(t, Limits{})
	f := &fakeForge{}
	f.set(forge.PullRequest{Number: 12, State: "OPEN", Draft: true, Checks: failingChecks}, nil)
	d.r.UseForge(f)
	if b := d.board()[d.job.ID]; b.PRStatus != nil {
		t.Fatalf("a Job with no PR has a reading: %+v", b.PRStatus)
	}
	if err := d.st.RecordPR(d.job.ID, sitePR); err != nil {
		t.Fatal(err)
	}
	d.r.refreshPRs(time.Now())
	b := d.board()[d.job.ID]
	if b.PR != sitePR || b.PRStatus == nil || b.PRStatus.CI != "failing" || !b.PRStatus.Draft || b.PRStatus.State != "OPEN" {
		t.Fatalf("%+v / %+v", b.Job, b.PRStatus)
	}
	code, body := d.do("GET", "/jobs/"+d.job.ID, "", nil)
	var job struct {
		PRStatus *PRStatus `json:"pr_status"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &job) != nil || job.PRStatus == nil || job.PRStatus.CI != "failing" {
		t.Fatalf("%d %s", code, body)
	}
}
