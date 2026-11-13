package works

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

func newRuns(t *testing.T) (*Runs, *store.Store) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRuns(st)
	r.poll = 5 * time.Millisecond
	t.Cleanup(func() { r.Close(); st.Close() })
	return r, st
}

func startRun(t *testing.T, r *Runs, role string) RunEnv {
	env, err := r.Start(role, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func call(t *testing.T, env RunEnv, id, tool, input string) spool.Response {
	t.Helper()
	resp, err := spool.Call(context.Background(), spool.Env{Run: env.Run, Dir: env.Spool, Token: env.Token}, id, tool, json.RawMessage(input), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAgentDrivesTheSpoolByFiles(t *testing.T) {
	r, _ := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	if fi, err := os.Stat(env.Spool); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("spool: %v %v", fi, err)
	}

	// An agent writes the request under a temp name, renames it, and polls for the response.
	req := `{"id":"r1","token":"` + env.Token + `","tool":"create_job","input":{"title":"Fix login"}}`
	tmp := filepath.Join(env.Spool, ".tmp-r1")
	if err := os.WriteFile(tmp, []byte(req), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(env.Spool, "r1.req.json")); err != nil {
		t.Fatal(err)
	}
	var resp spool.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(env.Spool, "r1.resp.json"))
		if err == nil {
			if err := json.Unmarshal(b, &resp); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no response")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var job store.Job
	if !resp.OK || json.Unmarshal(resp.Output, &job) != nil || job.Title != "Fix login" {
		t.Fatalf("%+v", resp)
	}
	// the Works removes the processed request
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(env.Spool, "r1.req.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("request not cleaned up: %v", err)
	}
}

func TestJobAndPlanTools(t *testing.T) {
	r, _ := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	var job store.Job
	resp := call(t, env, "a1", "create_job", `{"title":"T","description":"D"}`)
	json.Unmarshal(resp.Output, &job)
	if !resp.OK || job.ID == "" {
		t.Fatalf("%+v", resp)
	}
	if resp := call(t, env, "a2", "set_plan", `{"job_id":"`+job.ID+`","steps":[{"id":"s","guild":"g","title":"t"}]}`); !resp.OK {
		t.Fatalf("%+v", resp)
	}
	var shown JobWithPlan
	resp = call(t, env, "a3", "show_job", `{"id":"`+job.ID+`"}`)
	json.Unmarshal(resp.Output, &shown)
	if !resp.OK || shown.Plan == nil || len(shown.Plan.Steps) != 1 {
		t.Fatalf("%+v", resp)
	}
	if resp := call(t, env, "a4", "show_plan", `{"job_id":"`+job.ID+`"}`); !resp.OK {
		t.Fatalf("%+v", resp)
	}
	var jobs []store.Job
	resp = call(t, env, "a5", "list_jobs", ``)
	json.Unmarshal(resp.Output, &jobs)
	if !resp.OK || len(jobs) != 1 {
		t.Fatalf("%+v", resp)
	}
}

func TestDuplicateRequestIDIsIdempotent(t *testing.T) {
	r, st := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	first := call(t, env, "dup", "create_job", `{"title":"Once"}`)
	// the same id again, even with different content, repeats the answer and executes nothing
	second := call(t, env, "dup", "create_job", `{"title":"Twice"}`)
	if string(first.Output) != string(second.Output) || !second.OK {
		t.Fatalf("%s vs %s", first.Output, second.Output)
	}
	if jobs, _ := st.ListJobs(); len(jobs) != 1 {
		t.Fatalf("%d jobs", len(jobs))
	}
}

func TestResponsesAreNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	if ok, err := spool.WriteResponse(dir, spool.Response{ID: "x", OK: true}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := spool.WriteResponse(dir, spool.Response{ID: "x", Error: &spool.Error{Code: "late"}}); ok || err != nil {
		t.Fatal(ok, err)
	}
	if got, _ := spool.ReadResponse(dir, "x"); !got.OK {
		t.Fatalf("%+v", got)
	}
}

func TestWrongTokenAndRoleAreRejected(t *testing.T) {
	r, st := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	bad := env
	bad.Token = "not-the-token"
	if resp := call(t, bad, "b1", "create_job", `{"title":"X"}`); resp.OK || resp.Error.Code != spool.CodeUnauthorized {
		t.Fatalf("%+v", resp)
	}
	// another run's token does not work in this run's spool
	other := startRun(t, r, RoleArchwright)
	cross := env
	cross.Token = other.Token
	if resp := call(t, cross, "b2", "create_job", `{"title":"X"}`); resp.OK || resp.Error.Code != spool.CodeUnauthorized {
		t.Fatalf("%+v", resp)
	}
	wright := startRun(t, r, RoleWright)
	if resp := call(t, wright, "b3", "create_job", `{"title":"X"}`); resp.OK || resp.Error.Code != spool.CodeForbidden {
		t.Fatalf("%+v", resp)
	}
	if jobs, _ := st.ListJobs(); len(jobs) != 0 {
		t.Fatalf("rejected calls ran: %d jobs", len(jobs))
	}
	// a rejected token does not poison the id for the real caller
	if resp := call(t, env, "b1", "create_job", `{"title":"X"}`); !resp.OK {
		t.Fatalf("%+v", resp)
	}
}

func TestSchemaValidation(t *testing.T) {
	r, _ := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	for i, c := range []struct{ tool, input, code string }{
		{"create_job", `{"titel":"typo"}`, spool.CodeInvalidInput},
		{"create_job", `{"title":""}`, spool.CodeInvalidInput},
		{"create_job", `[1]`, spool.CodeInvalidInput},
		{"show_job", `{}`, spool.CodeInvalidInput},
		{"show_job", `{"id":"nope"}`, spool.CodeNotFound},
		{"set_plan", `{"job_id":"nope","steps":[]}`, spool.CodeNotFound},
		{"nope", `{}`, spool.CodeUnknownTool},
	} {
		id := "v" + string(rune('a'+i))
		if resp := call(t, env, id, c.tool, c.input); resp.OK || resp.Error.Code != c.code {
			t.Errorf("%s %s: %+v", c.tool, c.input, resp)
		}
	}
}

func TestMalformedAndHostileRequestFiles(t *testing.T) {
	r, _ := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	// garbage and an id/name mismatch get answered, not executed
	os.WriteFile(filepath.Join(env.Spool, "g1.req.json"), []byte("{nope"), 0o600)
	os.WriteFile(filepath.Join(env.Spool, "g2.req.json"), []byte(`{"id":"other","token":"`+env.Token+`","tool":"list_jobs"}`), 0o600)
	// a symlink to a file outside the spool is never followed
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte(`{"id":"g3","token":"`+env.Token+`","tool":"list_jobs"}`), 0o600)
	os.Symlink(secret, filepath.Join(env.Spool, "g3.req.json"))
	for _, id := range []string{"g1", "g2", "g3"} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			resp, err := spool.ReadResponse(env.Spool, id)
			if err == nil {
				if resp.OK || resp.Error.Code != spool.CodeBadRequest {
					t.Fatalf("%s: %+v", id, resp)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: no response", id)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestCallTimesOutAndWithdrawsRequest(t *testing.T) {
	dir := t.TempDir() // nobody is serving this spool
	env := spool.Env{Run: "r", Dir: dir, Token: "t"}
	_, err := spool.Call(context.Background(), env, "slow", "list_jobs", nil, 50*time.Millisecond)
	if !errors.Is(err, spool.ErrTimeout) {
		t.Fatalf("%v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestEndedRunSpoolIsRemovedAndNotRead(t *testing.T) {
	r, _ := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	call(t, env, "c1", "list_jobs", ``)
	if err := r.End(env.Run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.Spool); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("spool survives the run: %v", err)
	}
	if err := r.End(env.Run); err == nil {
		t.Fatal("ending twice should fail")
	}
}

func TestProcessedFilesAreCleanedUp(t *testing.T) {
	r, _ := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	call(t, env, "d1", "list_jobs", ``)
	time.Sleep(50 * time.Millisecond)
	if entries, _ := os.ReadDir(env.Spool); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestStartRejectsUnknownRole(t *testing.T) {
	r, _ := newRuns(t)
	if _, err := r.Start("root", t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
}
