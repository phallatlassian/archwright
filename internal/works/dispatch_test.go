package works

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

const goodBrief = `{"objective":"Add login","scope":{"in":["auth/"],"out":["billing/"]},"constraints":[],
"inputs":["docs/spec.md"],"definition_of_done":["tests pass"],"return_shape":"a branch name","limits":{"timeout_minutes":30}}`

type rig struct {
	t      *testing.T
	r      *Runs
	st     *store.Store
	runner *FakeRunner
	job    store.Job
	warden RunEnv
	arch   RunEnv
	n      int
}

func newRig(t *testing.T, script map[string][]WrightResult) *rig {
	r, st := newRuns(t)
	runner := NewFakeRunner(script)
	r.Configure(func(name string) (Guild, error) {
		if name != "engineering" {
			return Guild{}, errInvalidInput
		}
		return Guild{Name: name, Standards: "Write tests first."}, nil
	}, runner)
	job, err := st.CreateJob("J", "")
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: job.ID, Guild: "engineering"})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, r: r, st: st, runner: runner, job: job, warden: w, arch: startRun(t, r, RoleArchwright)}
}

func (g *rig) call(env RunEnv, tool, input string) spool.Response {
	g.n++
	return call(g.t, env, "c"+string(rune('a'+g.n/26))+string(rune('a'+g.n%26)), tool, input)
}

func (g *rig) ok(env RunEnv, tool, input string, out any) {
	g.t.Helper()
	resp := g.call(env, tool, input)
	if !resp.OK {
		g.t.Fatalf("%s: %+v", tool, resp.Error)
	}
	if out != nil {
		json.Unmarshal(resp.Output, out)
	}
}

func (g *rig) fails(env RunEnv, tool, input, code string) {
	g.t.Helper()
	if resp := g.call(env, tool, input); resp.OK || resp.Error.Code != code {
		g.t.Fatalf("%s %s: want %s, got %+v", tool, input, code, resp)
	}
}

func (g *rig) dispatch(piece string) DispatchInfo {
	var d DispatchInfo
	g.ok(g.warden, "dispatch_wright", `{"piece":"`+piece+`","brief":`+goodBrief+`}`, &d)
	g.waitReported(piece)
	return d
}

func (g *rig) waitReported(piece string) store.Dispatch {
	g.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if d, err := g.st.LatestDispatch(g.job.ID, piece); err == nil && d.Status != store.DispatchRunning {
			return d
		}
	}
	g.t.Fatal("wright never reported")
	return store.Dispatch{}
}

func (g *rig) rework(piece string) DispatchInfo {
	var d DispatchInfo
	g.ok(g.warden, "rework_wright", `{"piece":"`+piece+`","reason":"missed tests","diagnosis":"wright skipped the suite","brief":`+goodBrief+`}`, &d)
	if d.Status == store.DispatchRunning {
		g.waitReported(piece)
	}
	return d
}

var failedResult = WrightResult{Status: ResultFailed, Uncertainty: "could not make it pass"}
var doneResult = WrightResult{Status: ResultDone, Result: "branch x", Evidence: []string{"go test"}}

func TestIncompleteBriefIsRejected(t *testing.T) {
	g := newRig(t, nil)
	for name, brief := range map[string]string{
		"no objective":        `{"scope":{"in":["a"]},"constraints":[],"inputs":["i"],"definition_of_done":["d"],"return_shape":"r","limits":{"timeout_minutes":1}}`,
		"constraints missing": `{"objective":"o","scope":{"in":["a"]},"inputs":["i"],"definition_of_done":["d"],"return_shape":"r","limits":{"timeout_minutes":1}}`,
		"no limits":           `{"objective":"o","scope":{"in":["a"]},"constraints":[],"inputs":["i"],"definition_of_done":["d"],"return_shape":"r"}`,
		"blank done":          `{"objective":"o","scope":{"in":["a"]},"constraints":[],"inputs":["i"],"definition_of_done":[" "],"return_shape":"r","limits":{"timeout_minutes":1}}`,
		"unknown field":       `{"objective":"o","extra":1,"scope":{"in":["a"]},"constraints":[],"inputs":["i"],"definition_of_done":["d"],"return_shape":"r","limits":{"timeout_minutes":1}}`,
		"empty":               `{}`,
	} {
		resp := g.call(g.warden, "dispatch_wright", `{"piece":"p","brief":`+brief+`}`)
		if resp.OK || resp.Error.Code != spool.CodeInvalidInput {
			t.Errorf("%s: %+v", name, resp)
		}
	}
	// the message names everything missing at once
	resp := g.call(g.warden, "dispatch_wright", `{"piece":"p","brief":{}}`)
	for _, f := range []string{"objective", "scope.in", "constraints", "inputs", "definition_of_done", "return_shape", "limits"} {
		if !strings.Contains(resp.Error.Message, f) {
			t.Errorf("message does not name %s: %s", f, resp.Error.Message)
		}
	}
	g.fails(g.warden, "dispatch_wright", `{"piece":"p"}`, spool.CodeInvalidInput)
	if ds, _ := g.st.Dispatches(g.job.ID); len(ds) != 0 {
		t.Fatalf("rejected briefs were dispatched: %d", len(ds))
	}
	if len(g.runner.Requests()) != 0 {
		t.Fatal("a Wright ran")
	}
}

func TestDispatchStoresBriefRenderedWithGuildStandards(t *testing.T) {
	g := newRig(t, nil)
	d := g.dispatch("login")
	stored, err := g.st.GetDispatch(d.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Add login", "auth/", "billing/", "docs/spec.md", "tests pass", "a branch name", "30 minutes", "Write tests first.", "(none)", "report_result"} {
		if !strings.Contains(stored.Rendered, want) {
			t.Errorf("rendered brief lacks %q:\n%s", want, stored.Rendered)
		}
	}
	reqs := g.runner.Requests()
	if len(reqs) != 1 || reqs[0].Brief != stored.Rendered {
		t.Fatal("the Wright did not receive the stored brief")
	}
	// the Archwright can read it back
	var ds []store.Dispatch
	g.ok(g.arch, "show_dispatches", `{"job_id":"`+g.job.ID+`"}`, &ds)
	if len(ds) != 1 || ds[0].Rendered != stored.Rendered {
		t.Fatalf("%+v", ds)
	}
	// a piece is dispatched once; further rounds are rework
	g.fails(g.warden, "dispatch_wright", `{"piece":"login","brief":`+goodBrief+`}`, spool.CodeConflict)
}

func TestWrightResultIsSchemaChecked(t *testing.T) {
	g := newRig(t, nil)
	// a Wright that holds the run's credentials but sends a bad shape is refused
	w, err := g.r.StartFor(RoleWright, t.TempDir(), RunContext{Job: g.job.ID, Guild: "engineering", DispatchID: "none"})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{
		`{"status":"great"}`,
		`{"status":"done"}`,
		`{"status":"done","result":"x"}`,
		`{"status":"blocked"}`,
		`{"status":"done","result":"x","evidence":["e"],"flags":[]}`,
	} {
		g.fails(w, "report_result", in, spool.CodeInvalidInput)
	}
	// the Wright cannot use Warden or Archwright tools
	g.fails(w, "handback", `{}`, spool.CodeForbidden)
	g.fails(w, "list_flags", `{"job_id":"x"}`, spool.CodeForbidden)
	g.fails(w, "dispatch_wright", `{}`, spool.CodeForbidden)
	// a second report for the same dispatch is a conflict
	d := g.dispatch("p")
	run, _ := g.st.GetDispatch(d.DispatchID)
	if run.Status != store.DispatchReported {
		t.Fatalf("%+v", run)
	}
	if err := g.st.RecordResult(d.DispatchID, json.RawMessage(`{}`)); err == nil {
		t.Fatal("second result accepted")
	}
}

func TestRunnerFailureFailsTheDispatch(t *testing.T) {
	r, st := newRuns(t)
	r.Configure(func(string) (Guild, error) { return Guild{Name: "engineering"}, nil }, silentRunner{})
	job, _ := st.CreateJob("J", "")
	w, _ := r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: job.ID, Guild: "engineering"})
	if resp := call(t, w, "x", "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`); !resp.OK {
		t.Fatalf("%+v", resp)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if d, _ := st.LatestDispatch(job.ID, "p"); d.Status == store.DispatchReported {
			if !strings.Contains(string(d.Result), "without reporting") {
				t.Fatalf("%s", d.Result)
			}
			return
		}
	}
	t.Fatal("dispatch never failed")
}

type silentRunner struct{}

func (silentRunner) Run(_ context.Context, _ RunRequest) error { return nil }

func TestReworkIsCountedPerPieceThenFails(t *testing.T) {
	g := newRig(t, map[string][]WrightResult{
		"a": {failedResult, failedResult, failedResult},
		"b": {failedResult, doneResult},
	})
	g.dispatch("a")
	g.dispatch("b")

	// a: two reworks are allowed, each a fresh run with an amended brief naming the reason
	r1 := g.rework("a")
	r2 := g.rework("a")
	if r1.Round != 1 || r2.Round != 2 || r2.Status != store.DispatchRunning || r1.DispatchID == r2.DispatchID {
		t.Fatalf("%+v %+v", r1, r2)
	}
	stored, _ := g.st.GetDispatch(r2.DispatchID)
	if !strings.Contains(stored.Rendered, "Rework round 2") || !strings.Contains(stored.Rendered, "missed tests") {
		t.Fatalf("%s", stored.Rendered)
	}
	// the third rejection fails the piece with reason, last result and diagnosis; no new run
	runs := len(g.runner.Requests())
	r3 := g.rework("a")
	if r3.Status != store.PieceFailed || r3.DispatchID != "" || len(g.runner.Requests()) != runs {
		t.Fatalf("%+v", r3)
	}
	var insp Inspection
	g.ok(g.warden, "inspect_wright", `{"piece":"a"}`, &insp)
	if insp.Piece.Status != store.PieceFailed || insp.Piece.Reworks != 2 || insp.Piece.Reason != "missed tests" ||
		insp.Piece.Diagnosis != "wright skipped the suite" || len(insp.Dispatches) != 3 || !strings.Contains(string(insp.Dispatches[2].Result), "could not make it pass") {
		t.Fatalf("%+v", insp)
	}
	g.fails(g.warden, "rework_wright", `{"piece":"a","reason":"again","brief":`+goodBrief+`}`, spool.CodeConflict)

	// b has its own count: one rework, then done
	if b := g.rework("b"); b.Round != 1 {
		t.Fatalf("%+v", b)
	}
	g.ok(g.warden, "accept_piece", `{"piece":"b"}`, nil)
	// a failed result cannot be accepted
	g.fails(g.warden, "accept_piece", `{"piece":"a"}`, spool.CodeConflict)
	// an amended brief is held to the same contract
	g.fails(g.warden, "rework_wright", `{"piece":"b","reason":"r","brief":{}}`, spool.CodeInvalidInput)
}

func TestReworkLimitIsPerGuild(t *testing.T) {
	r, st := newRuns(t)
	r.Configure(func(string) (Guild, error) { return Guild{Name: "engineering", ReworkRounds: 1}, nil },
		NewFakeRunner(map[string][]WrightResult{"p": {failedResult, failedResult, failedResult}}))
	job, _ := st.CreateJob("J", "")
	g := &rig{t: t, r: r, st: st, job: job}
	g.warden, _ = r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: job.ID, Guild: "engineering"})
	g.dispatch("p")
	if d := g.rework("p"); d.Round != 1 {
		t.Fatalf("%+v", d)
	}
	if d := g.rework("p"); d.Status != store.PieceFailed {
		t.Fatalf("%+v", d)
	}
}

func handbackJSON(status, results, extra string) string {
	return `{"status":"` + status + `","results":[` + results + `],"checked":["ran the suite"],"rejected":[],"uncertainty":"none"` + extra + `}`
}

func TestHandbackShapeIsEnforced(t *testing.T) {
	g := newRig(t, map[string][]WrightResult{"p": {doneResult}, "q": {failedResult}})
	g.dispatch("p")
	g.dispatch("q")
	for name, in := range map[string]string{
		"unknown status": handbackJSON("great", `"x"`, ""),
		"unknown field":  handbackJSON("partial", `"x"`, `,"gates":[]`),
		"no checked":     `{"status":"partial","results":["x"]}`,
		"no results":     handbackJSON("partial", ``, ""),
		"bad flag kind":  handbackJSON("partial", `"x"`, `,"flags":[{"kind":"gossip","description":"d","evidence":"e"}]`),
		"flag no proof":  handbackJSON("partial", `"x"`, `,"flags":[{"kind":"risk","description":"d"}]`),
		"done early":     handbackJSON("done", `"x"`, ""), // nothing accepted yet
		"partial early":  handbackJSON("partial", `"x"`, ""),
	} {
		if resp := g.call(g.warden, "handback", in); resp.OK || resp.Error.Code != spool.CodeInvalidInput {
			t.Errorf("%s: %+v", name, resp)
		}
	}
	if _, err := g.st.LatestHandback(g.job.ID); err == nil {
		t.Fatal("a bad handback was stored")
	}
	// a handback before anything is dispatched has nothing to hand back
	empty := newRig(t, nil)
	empty.fails(empty.warden, "handback", handbackJSON("done", `"x"`, ""), spool.CodeInvalidInput)
}

func TestPartialHandbackKeepsAcceptedPiecesAndParksTheJob(t *testing.T) {
	g := newRig(t, map[string][]WrightResult{"p": {doneResult}, "q": {failedResult, failedResult, failedResult}})
	g.dispatch("p")
	g.dispatch("q")
	g.ok(g.warden, "accept_piece", `{"piece":"p"}`, nil)
	g.rework("q")
	g.rework("q")
	if d := g.rework("q"); d.Status != store.PieceFailed {
		t.Fatalf("%+v", d)
	}
	if j, _ := g.st.GetJob(g.job.ID); j.ParkedFor != "" {
		t.Fatal("parked early")
	}
	// "done" is refused while a piece failed; "failed" is refused while one is accepted
	g.fails(g.warden, "handback", handbackJSON("done", `"branch x"`, ""), spool.CodeInvalidInput)
	g.fails(g.warden, "handback", handbackJSON("failed", ``, ""), spool.CodeInvalidInput)

	var out map[string]any
	g.ok(g.warden, "handback", handbackJSON("partial", `"branch x"`, ""), &out)
	if out["parked"] != true {
		t.Fatalf("%v", out)
	}
	job, _ := g.st.GetJob(g.job.ID)
	if job.ParkedFor == "" {
		t.Fatal("job not parked")
	}
	ps, _ := g.st.Pieces(g.job.ID)
	if ps[0].Status != store.PieceAccepted || ps[1].Status != store.PieceFailed {
		t.Fatalf("%+v", ps)
	}
	var shown struct {
		Status string
		Body   HandbackBody
	}
	g.ok(g.arch, "show_handback", `{"job_id":"`+g.job.ID+`"}`, &shown)
	if shown.Status != HandbackPartial || len(shown.Body.Pieces) != 2 {
		t.Fatalf("%+v", shown)
	}
}

func TestDoneHandbackNeedsEveryPieceAccepted(t *testing.T) {
	g := newRig(t, nil)
	g.dispatch("p")
	g.fails(g.warden, "handback", handbackJSON("done", `"x"`, ""), spool.CodeInvalidInput)
	g.ok(g.warden, "accept_piece", `{"piece":"p"}`, nil)
	g.ok(g.warden, "handback", handbackJSON("done", `"x"`, ""), nil)
	if j, _ := g.st.GetJob(g.job.ID); j.ParkedFor != "" {
		t.Fatal("a done handback parked the Job")
	}
}

func TestFlagsReachTheArchwrightOnly(t *testing.T) {
	g := newRig(t, map[string][]WrightResult{"p": {{Status: ResultDone, Result: "x", Evidence: []string{"e"}, Observations: []string{"billing looks wrong"}}}})
	g.dispatch("p")
	g.ok(g.warden, "accept_piece", `{"piece":"p"}`, nil)
	flags := `,"flags":[{"kind":"follow-up","description":"billing looks wrong","evidence":"billing/x.go:4"},{"kind":"decision-needed","description":"which db","evidence":"docs/db.md"},{"kind":"risk","description":"no rollback","evidence":"deploy.md"}]`
	var out map[string]any
	g.ok(g.warden, "handback", handbackJSON("done", `"x"`, flags), &out)
	if out["flags_recorded"] != float64(3) {
		t.Fatalf("%v", out)
	}

	var fl []store.Flag
	g.ok(g.arch, "list_flags", `{"job_id":"`+g.job.ID+`"}`, &fl)
	if len(fl) != 3 || fl[0].Kind != "follow-up" || fl[2].Evidence != "deploy.md" {
		t.Fatalf("%+v", fl)
	}
	var shown struct{ Flags []store.Flag }
	g.ok(g.arch, "show_handback", `{"job_id":"`+g.job.ID+`"}`, &shown)
	if len(shown.Flags) != 3 {
		t.Fatalf("%+v", shown)
	}

	// no Warden or Wright call can read them: the tools are Archwright-only, and nothing a Warden sees carries them
	g.fails(g.warden, "list_flags", `{"job_id":"`+g.job.ID+`"}`, spool.CodeForbidden)
	g.fails(g.warden, "show_handback", `{"job_id":"`+g.job.ID+`"}`, spool.CodeForbidden)
	resp := g.call(g.warden, "inspect_wright", `{"piece":"p"}`)
	if !resp.OK || strings.Contains(string(resp.Output), "no rollback") || strings.Contains(string(resp.Output), "flags") {
		t.Fatalf("%s", resp.Output)
	}
	// Wright observations go to the Warden, in its inspection
	if !strings.Contains(string(resp.Output), "billing looks wrong") {
		t.Fatalf("%s", resp.Output)
	}
	wright := startRun(t, g.r, RoleWright)
	g.fails(wright, "list_flags", `{"job_id":"`+g.job.ID+`"}`, spool.CodeForbidden)
	// the Archwright cannot dispatch, hand back or report
	g.fails(g.arch, "handback", handbackJSON("done", `"x"`, ""), spool.CodeForbidden)
	g.fails(g.arch, "dispatch_wright", `{}`, spool.CodeForbidden)
}

func TestWardenIsScopedToItsJobAndGuild(t *testing.T) {
	g := newRig(t, nil)
	g.dispatch("p")
	// a Warden run bound to nothing cannot dispatch
	loose := startRun(t, g.r, RoleWarden)
	g.fails(loose, "dispatch_wright", `{"piece":"x","brief":`+goodBrief+`}`, spool.CodeInvalidInput)
	// another Job's Warden cannot see this Job's piece
	other, _ := g.st.CreateJob("other", "")
	ow, _ := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: other.ID, Guild: "engineering"})
	g.fails(ow, "inspect_wright", `{"piece":"p"}`, spool.CodeNotFound)
	g.fails(ow, "accept_piece", `{"piece":"p"}`, spool.CodeNotFound)
	// an unknown Guild cannot dispatch
	bad, _ := g.r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: g.job.ID, Guild: "wizards"})
	g.fails(bad, "dispatch_wright", `{"piece":"x","brief":`+goodBrief+`}`, spool.CodeInvalidInput)
}

func TestDispatchFailsClosedWithoutRunner(t *testing.T) {
	r, st := newRuns(t)
	r.Configure(func(string) (Guild, error) { return Guild{Name: "engineering"}, nil }, nil)
	job, _ := st.CreateJob("J", "")
	w, _ := r.StartFor(RoleWarden, t.TempDir(), RunContext{Job: job.ID, Guild: "engineering"})
	if resp := call(t, w, "x", "dispatch_wright", `{"piece":"p","brief":`+goodBrief+`}`); resp.OK || resp.Error.Code != spool.CodeInvalidInput {
		t.Fatalf("%+v", resp)
	}
}

func TestGuildsFromDir(t *testing.T) {
	dir := t.TempDir()
	if err := writeGuild(dir, "engineering", "Be kind."); err != nil {
		t.Fatal(err)
	}
	src := GuildsFromDir(dir)
	g, err := src("engineering")
	if err != nil || g.Standards != "Be kind." {
		t.Fatalf("%+v %v", g, err)
	}
	for _, name := range []string{"", "nope", "../x", ".hidden"} {
		if _, err := src(name); err == nil {
			t.Errorf("%q resolved", name)
		}
	}
}

func writeGuild(dir, name, standards string) error {
	if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name, "standards.md"), []byte(standards), 0o600)
}
