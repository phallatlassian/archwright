package works

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

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

// newHandler serves the API. done closes when the Works shuts down, so streams end.
func newHandler(st *store.Store, runs *Runs, sm *sites.Manager, token string, pid int, shutdown func(), done <-chan struct{}) http.Handler {
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

	// mechanical wraps a dashboard command the Works executes at once (ADR 0007):
	// the command is recorded as an event first, executed, then reported to the
	// Archwright's inbox. target names the Job it concerns, or fails if there is none.
	mechanical := func(name string, target func(r *http.Request) (string, error), do func(r *http.Request, body []byte) (any, string, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
			if err != nil {
				fail(w, err)
				return
			}
			jobID, err := target(r)
			if err != nil {
				fail(w, err)
				return
			}
			eventID, err := st.RecordCommand(name, jobID, argsOf(body))
			if err != nil {
				fail(w, err)
				return
			}
			res, summary, err := do(r, body)
			if err != nil {
				if ferr := st.FailCommand(eventID, name, jobID, err); ferr != nil {
					log.Printf("command %s: recording failure: %v", name, ferr)
				}
				fail(w, err)
				return
			}
			eventJob := jobID
			if name == "delete_job" {
				eventJob = "" // the Job and its events are gone
			}
			if err := st.ReportCommand(eventID, name, eventJob, jobID, summary, res); err != nil {
				log.Printf("command %s: reporting to the Archwright: %v", name, err)
			}
			reply(w, 200, res)
		}
	}
	// judgment wraps a command that needs the Archwright's judgment: it is
	// recorded as an event and filed in the Archwright's inbox, and nothing runs.
	judgment := func(name string, needJob bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Text         string `json:"text"`
				Instructions string `json:"instructions"`
				Piece        string `json:"piece"`
				Jig          string `json:"jig"`
				Note         string `json:"note"`
			}
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				fail(w, err)
				return
			}
			jobID := r.PathValue("id")
			if jobID != "" {
				if _, err := st.GetJob(jobID); err != nil {
					fail(w, err)
					return
				}
			}
			var summary string
			switch name {
			case "instruct":
				if strings.TrimSpace(in.Text) == "" {
					fail(w, errors.New("text is required"))
					return
				}
				summary = "The developer says: " + in.Text
			case "retry_job":
				if strings.TrimSpace(in.Instructions) == "" {
					fail(w, errors.New("instructions are required"))
					return
				}
				summary = "The developer asks to retry with new instructions: " + in.Instructions
			case "replan":
				if strings.TrimSpace(in.Instructions) == "" {
					fail(w, errors.New("instructions are required"))
					return
				}
				summary = "The developer asks for a new Plan: " + in.Instructions
			case "change_jig":
				if strings.TrimSpace(in.Jig) == "" {
					fail(w, errors.New("jig is required"))
					return
				}
				summary = "The developer asks to change the Jig to " + in.Jig
				if in.Note != "" {
					summary += ": " + in.Note
				}
			}
			args, _ := json.Marshal(in)
			eventID, err := st.QueueCommand(name, jobID, summary, args)
			if err != nil {
				fail(w, err)
				return
			}
			reply(w, 202, map[string]any{"queued": true, "event_id": eventID, "job_id": jobID})
		}
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
	jobTarget := func(r *http.Request) (string, error) {
		j, err := st.GetJob(r.PathValue("id"))
		return j.ID, err
	}
	mux.HandleFunc("POST /jobs/{id}/plan/approve", mechanical("approve_plan", jobTarget, func(r *http.Request, body []byte) (any, string, error) {
		var in struct {
			UpdatedAt time.Time `json:"updated_at"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.UpdatedAt.IsZero() {
			return nil, "", errors.New("updated_at of the reviewed Plan is required")
		}
		p, err := st.DecidePlan(r.PathValue("id"), true, in.UpdatedAt)
		return p, "The developer approved the Plan of job " + r.PathValue("id") + "; work may be dispatched.", err
	}))
	mux.HandleFunc("POST /jobs/{id}/plan/reject", mechanical("reject_plan", jobTarget, func(r *http.Request, body []byte) (any, string, error) {
		p, err := st.DecidePlan(r.PathValue("id"), false, time.Time{})
		return p, "The developer rejected the Plan of job " + r.PathValue("id") + "; it needs a new Plan.", err
	}))

	// Jobs the developer controls from the dashboard (ADR 0007).
	mux.HandleFunc("POST /jobs/{id}/cancel", mechanical("cancel_job", jobTarget, func(r *http.Request, body []byte) (any, string, error) {
		res, err := runs.CancelJob(r.PathValue("id"))
		return res, "The developer cancelled job " + r.PathValue("id") + ": its Wrights were stopped and it was discarded.", err
	}))
	mux.HandleFunc("POST /jobs/{id}/pause", mechanical("pause_job", jobTarget, func(r *http.Request, body []byte) (any, string, error) {
		j, err := runs.PauseJob(r.PathValue("id"))
		return j, "The developer paused job " + r.PathValue("id") + ": no new Wright starts until it is resumed.", err
	}))
	mux.HandleFunc("POST /jobs/{id}/resume", mechanical("resume_job", jobTarget, func(r *http.Request, body []byte) (any, string, error) {
		j, err := runs.ResumeJob(r.PathValue("id"))
		return j, "The developer resumed job " + r.PathValue("id") + ".", err
	}))
	mux.HandleFunc("POST /jobs/{id}/purge", mechanical("purge_job", jobTarget, func(r *http.Request, body []byte) (any, string, error) {
		j, err := runs.PurgeJob(r.PathValue("id"))
		return j, "The developer purged the raw run content of job " + r.PathValue("id") + "; its summary is kept.", err
	}))
	mux.HandleFunc("DELETE /jobs/{id}", mechanical("delete_job", jobTarget, func(r *http.Request, body []byte) (any, string, error) {
		res, err := runs.DeleteJob(r.PathValue("id"))
		return res, "The developer deleted job " + r.PathValue("id") + " entirely.", err
	}))
	mux.HandleFunc("GET /jobs/{id}/dispatches", func(w http.ResponseWriter, r *http.Request) {
		ds, err := st.Dispatches(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, ds)
	})
	mux.HandleFunc("POST /dispatches/{id}/cancel", mechanical("cancel_wright", func(r *http.Request) (string, error) {
		d, err := st.GetDispatch(r.PathValue("id"))
		return d.JobID, err
	}, func(r *http.Request, body []byte) (any, string, error) {
		res, err := runs.CancelDispatch(r.PathValue("id"))
		return res, "The developer cancelled the Wright of dispatch " + r.PathValue("id") + ".", err
	}))

	// Commands that need judgment go to the Archwright's inbox, not to a Guild.
	mux.HandleFunc("POST /instruct", judgment("instruct", false))
	mux.HandleFunc("POST /jobs/{id}/instruct", judgment("instruct", true))
	mux.HandleFunc("POST /jobs/{id}/retry", judgment("retry_job", true))
	mux.HandleFunc("POST /jobs/{id}/replan", judgment("replan", true))
	mux.HandleFunc("POST /jobs/{id}/change-jig", judgment("change_jig", true))
	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, r *http.Request) {
		items, err := st.Inbox(r.URL.Query().Get("unread") != "")
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, items)
	})

	// Events: a page after a cursor, and the same as a one-way stream.
	mux.HandleFunc("GET /events", eventsPage(st, reply, fail))
	mux.HandleFunc("GET /events/stream", eventsStream(st, done, fail))
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

	// Retention (ADR 0017): prune raw run content, and what an upgrade would interrupt.
	mux.HandleFunc("POST /prune", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DryRun bool `json:"dry_run"`
			Days   int  `json:"days"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			fail(w, err)
			return
		}
		rep, err := runs.Prune(in.Days, in.DryRun)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, rep)
	})
	mux.HandleFunc("GET /runs/active", func(w http.ResponseWriter, r *http.Request) {
		n, err := runs.ActiveRuns()
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]int{"active": n})
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
	askTarget := func(r *http.Request) (string, error) {
		a, err := st.GetAsk(r.PathValue("id"))
		return a.JobID, err
	}
	mux.HandleFunc("POST /asks/{id}/answer", mechanical("answer_ask", askTarget, func(r *http.Request, body []byte) (any, string, error) {
		var in struct {
			Answers []store.Answer `json:"answers"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, "", err
		}
		res, err := runs.AnswerAsk(r.PathValue("id"), RoleArchwright, AnsweredByDashboard, in.Answers)
		return res, "The developer answered ask " + r.PathValue("id") + "; the parked work resumes.", err
	}))
	mux.HandleFunc("POST /asks/{id}/accept-default", mechanical("accept_ask_default", askTarget, func(r *http.Request, body []byte) (any, string, error) {
		res, err := runs.AcceptDefault(r.PathValue("id"), AnsweredByDashboard)
		return res, "The developer accepted the recommended defaults for ask " + r.PathValue("id") + ".", err
	}))
	mux.HandleFunc("POST /asks/{id}/skip", mechanical("skip_ask", askTarget, func(r *http.Request, body []byte) (any, string, error) {
		res, err := runs.SkipAsk(r.PathValue("id"), AnsweredByDashboard)
		return res, "The developer skipped ask " + r.PathValue("id") + ".", err
	}))
	mux.HandleFunc("POST /asks/{id}/cancel", mechanical("cancel_ask", askTarget, func(r *http.Request, body []byte) (any, string, error) {
		res, err := runs.CancelAsk(r.PathValue("id"), AnsweredByDashboard)
		return res, "The developer cancelled ask " + r.PathValue("id") + " and its Job.", err
	}))

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

	return guard(token, mux)
}

// maxBody caps a request body; commands are small.
const maxBody = 1 << 20

// argsOf makes a request body storable as a command's arguments.
func argsOf(body []byte) json.RawMessage {
	if len(body) == 0 {
		return json.RawMessage("{}")
	}
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	raw, _ := json.Marshal(string(body))
	return raw
}

// guard is the API's edge (ADR 0007). Every request needs the per-install
// token, and the Host must be a loopback name, so a page that rebinds its own
// DNS name to 127.0.0.1 gets nothing. A command (anything but a read) from
// another origin is refused even with the token.
func guard(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, `{"error":"forbidden host"}`, http.StatusForbidden)
			return
		}
		ok := subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) == 1
		// A browser's EventSource cannot set headers, so the stream also takes the token as a query parameter.
		if !ok && r.Method == http.MethodGet && r.URL.Path == "/events/stream" {
			ok = subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) == 1
		}
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if isCommand(r.Method) && crossOrigin(r) {
			http.Error(w, `{"error":"cross-origin commands are refused"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isCommand(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// crossOrigin says a browser sent the request from a page that is not this
// server's own. Requests without browser headers (the CLI, curl) are not.
func crossOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err != nil || u.Scheme != "http" || u.Host != r.Host
}
