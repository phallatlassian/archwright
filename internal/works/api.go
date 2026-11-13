package works

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

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

func newHandler(st *store.Store, runs *Runs, token string, pid int, shutdown func()) http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, err error) {
		code := http.StatusBadRequest // validation failures; genuine database errors are rare enough to share it
		if errors.Is(err, store.ErrNotFound) {
			code = http.StatusNotFound
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
		var in struct{ Title, Description string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		j, err := st.CreateJob(in.Title, in.Description)
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

	// The adapters start and end runs through these; each run gets its own spool and token.
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Role, SpoolRoot string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		env, err := runs.Start(in.Role, in.SpoolRoot)
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
