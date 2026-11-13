package works

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/store"
)

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Health is the /health response.
type Health struct {
	PID           int `json:"pid"`
	SchemaVersion int `json:"schema_version"`
}

// JobWithPlan is the /jobs/{id} response; Plan is nil until one is set.
type JobWithPlan struct {
	store.Job
	Plan *store.Plan `json:"plan"`
	// Asks are the Job's asks still waiting for an answer; BlockedOnYou says
	// one of them waits on the developer (ADR 0015).
	Asks         []store.Ask `json:"asks"`
	BlockedOnYou bool        `json:"blocked_on_you"`
}

func newHandler(st *store.Store, runs *Runs, sm *sites.Manager, token string, pid int, shutdown func()) http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, err error) {
		code := http.StatusBadRequest // validation failures; genuine database errors are rare enough to share it
		switch {
		case errors.Is(err, store.ErrNotFound):
			code = http.StatusNotFound
		case errors.Is(err, store.ErrExists), errors.Is(err, store.ErrConflict):
			code = http.StatusConflict
		case errors.Is(err, sites.ErrUnregistered):
			code = http.StatusForbidden
		}
		reply(w, code, map[string]string{"error": err.Error()})
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, Health{PID: pid, SchemaVersion: store.SchemaVersion()})
	})
	mux.HandleFunc("POST /shutdown", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]bool{"ok": true})
		go shutdown()
	})
	mux.HandleFunc("POST /jobs", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Title, Description, Site, Path string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		var j store.Job
		var err error
		switch {
		case in.Path != "": // the directory the developer launched from
			var site store.Site
			if site, err = sm.Resolve(in.Path); err == nil {
				j, err = st.CreateJobOn(site.Name, in.Title, in.Description)
			}
		case in.Site != "":
			j, err = st.CreateJobOn(in.Site, in.Title, in.Description)
		default:
			j, err = st.CreateJob(in.Title, in.Description)
		}
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 201, j)
	})
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs, err := st.ListJobs()
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, jobs)
	})
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := jobWithPlan(st, r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("PUT /jobs/{id}/plan", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Steps []store.Step `json:"steps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		p, err := st.SetPlan(r.PathValue("id"), in.Steps)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, p)
	})
	// Jig Plans start without approval; an ad hoc Plan waits for the
	// developer's go or no-go (ADR 0009).
	mux.HandleFunc("POST /jobs/{id}/jig", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Jig    string            `json:"jig"`
			Params map[string]string `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		p, err := instantiateJig(st, runs.Definitions(), r.PathValue("id"), in.Jig, in.Params)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, p)
	})
	mux.HandleFunc("POST /jobs/{id}/plan/approve", func(w http.ResponseWriter, r *http.Request) {
		p, err := st.DecidePlan(r.PathValue("id"), true)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, p)
	})
	mux.HandleFunc("POST /jobs/{id}/plan/reject", func(w http.ResponseWriter, r *http.Request) {
		p, err := st.DecidePlan(r.PathValue("id"), false)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, p)
	})
	mux.HandleFunc("GET /catalogue", func(w http.ResponseWriter, r *http.Request) {
		reg := runs.Definitions()
		if reg == nil {
			fail(w, errors.New("no definitions are loaded"))
			return
		}
		reply(w, 200, catalogueOf(reg))
	})
	mux.HandleFunc("POST /catalogue/reload", func(w http.ResponseWriter, r *http.Request) {
		reg := runs.Definitions()
		if reg == nil {
			fail(w, errors.New("no definitions are loaded"))
			return
		}
		reply(w, 200, ReloadDefinitions(reg))
	})
	mux.HandleFunc("GET /jobs/{id}/plan", func(w http.ResponseWriter, r *http.Request) {
		p, err := st.GetPlan(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, p)
	})

	// Reviews (ADR 0012): the Archwright links the writ and requests, reworks and
	// green-lights through its tools; the dashboard reads the same state.
	mux.HandleFunc("PUT /jobs/{id}/writ", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Writ string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		j, err := st.SetWrit(r.PathValue("id"), in.Writ)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, j)
	})
	mux.HandleFunc("GET /jobs/{id}/reviews", func(w http.ResponseWriter, r *http.Request) {
		j, err := st.GetJob(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reviews, err := st.Reviews(j.ID)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, ReviewState{Reviews: reviews, Next: runs.reviewNext(j, reviews)})
	})

	// The dispatch scheduler's running and queued Wrights, and the Needs-you inbox.
	mux.HandleFunc("GET /queue", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, runs.Queue())
	})
	mux.HandleFunc("GET /needs-you", func(w http.ResponseWriter, r *http.Request) {
		items, err := st.NeedsYou()
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, items)
	})

	// Asks (ADR 0015): the dashboard answers through the same Works path as
	// the Archwright's tool calls, so the first answer wins on either surface.
	mux.HandleFunc("GET /asks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		list, err := st.Asks(store.AskFilter{Job: q.Get("job"), Status: q.Get("status"), Holder: q.Get("holder")})
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, list)
	})
	mux.HandleFunc("GET /asks/{id}", func(w http.ResponseWriter, r *http.Request) {
		a, err := st.GetAsk(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, a)
	})
	mux.HandleFunc("POST /asks/{id}/answer", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Answers []store.Answer `json:"answers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		res, err := runs.AnswerAsk(r.PathValue("id"), RoleArchwright, AnsweredByDashboard, in.Answers)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, res)
	})
	mux.HandleFunc("POST /asks/{id}/accept-default", func(w http.ResponseWriter, r *http.Request) {
		res, err := runs.AcceptDefault(r.PathValue("id"), AnsweredByDashboard)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, res)
	})
	mux.HandleFunc("POST /asks/{id}/skip", func(w http.ResponseWriter, r *http.Request) {
		res, err := runs.SkipAsk(r.PathValue("id"), AnsweredByDashboard)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, res)
	})
	mux.HandleFunc("POST /asks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		res, err := runs.CancelAsk(r.PathValue("id"), AnsweredByDashboard)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, res)
	})

	mux.HandleFunc("POST /sites", func(w http.ResponseWriter, r *http.Request) {
		var in sites.RegisterInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		site, err := sm.Register(in)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 201, site)
	})
	mux.HandleFunc("GET /sites", func(w http.ResponseWriter, r *http.Request) {
		list, err := st.ListSites()
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, list)
	})
	// A Site's gates change only through a proposal the developer approves (ADR 0010).
	mux.HandleFunc("POST /sites/{name}/gates", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Typecheck, Lint, Test *string } // unset keeps the current command
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		site, err := st.GetSite(r.PathValue("name"))
		if err != nil {
			fail(w, err)
			return
		}
		g := site.Gates
		for dst, src := range map[*string]*string{&g.Typecheck: in.Typecheck, &g.Lint: in.Lint, &g.Test: in.Test} {
			if src != nil {
				*dst = *src
			}
		}
		c, err := st.ProposeGates(site.Name, g)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 201, c)
	})
	mux.HandleFunc("GET /gate-changes", func(w http.ResponseWriter, r *http.Request) {
		list, err := st.GateChanges(r.URL.Query().Get("status"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, list)
	})
	mux.HandleFunc("POST /gate-changes/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		site, err := st.ApproveGateChange(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, site)
	})
	mux.HandleFunc("POST /gate-changes/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		if err := st.RejectGateChange(r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /sites/detect", func(w http.ResponseWriter, r *http.Request) {
		p, err := sm.Detect(r.URL.Query().Get("url"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, p)
	})
	mux.HandleFunc("DELETE /sites/{name}", func(w http.ResponseWriter, r *http.Request) {
		site, err := sm.Remove(r.PathValue("name"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, site)
	})
	mux.HandleFunc("GET /sites/resolve", func(w http.ResponseWriter, r *http.Request) {
		site, err := sm.Resolve(r.URL.Query().Get("path"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, site)
	})
	mux.HandleFunc("POST /jobs/{id}/worktrees", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Kind    string `json:"kind"`
			BaseRef string `json:"base_ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		wt, err := sm.CreateWorktree(r.PathValue("id"), in.Kind, in.BaseRef)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 201, wt)
	})
	mux.HandleFunc("GET /jobs/{id}/worktrees", func(w http.ResponseWriter, r *http.Request) {
		wts, err := st.Worktrees(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, wts)
	})
	mux.HandleFunc("POST /jobs/{id}/worktrees/cleanup", func(w http.ResponseWriter, r *http.Request) {
		removed, err := sm.CleanupMerged(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, removed)
	})
	mux.HandleFunc("POST /jobs/{id}/close", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Outcome string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		removed, err := sm.Close(r.PathValue("id"), in.Outcome)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, removed)
	})

	// The adapters start and end runs through these; each run gets its own spool and token.
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Role, SpoolRoot, Job, Guild string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		env, err := runs.StartFor(in.Role, in.SpoolRoot, RunContext{Job: in.Job, Guild: in.Guild})
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 201, env)
	})
	mux.HandleFunc("DELETE /runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := runs.End(r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	})

	return requireToken(token, mux)
}

func requireToken(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
