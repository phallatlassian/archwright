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
		case errors.Is(err, store.ErrExists):
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
	mux.HandleFunc("GET /jobs/{id}/plan", func(w http.ResponseWriter, r *http.Request) {
		p, err := st.GetPlan(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, p)
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
