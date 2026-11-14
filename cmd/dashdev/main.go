// Command dashdev serves the embedded dashboard UI against mock data, for
// iterating on it without a running Works.
//
//	make dash            # then open the printed URL
//
// The UI files are read from disk on every request (not from the embedded
// copy), and the page reloads itself when one changes; CSS changes swap in
// without a reload. The API is a mock built from the Works' own response
// types (mock.go). Commands such as approve, pause and answer change the
// in-memory state, so the flows can be clicked through; restart to reset.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
	"github.com/phallatlassian/archwright/internal/works"
)

const token = "dev"

func main() {
	addr := flag.String("addr", "127.0.0.1:5173", "address to listen on")
	dir := flag.String("ui", "internal/works/ui", "directory holding the dashboard files")
	scenario := flag.String("scenario", "full", "mock data set: full or empty")
	flag.Parse()
	if _, err := os.Stat(filepath.Join(*dir, "index.html")); err != nil {
		log.Fatalf("no dashboard files in %s (run from the repository root, or pass -ui): %v", *dir, err)
	}
	if *scenario != "full" && *scenario != "empty" {
		log.Fatalf("unknown scenario %q (want full or empty)", *scenario)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{world: newWorld(*scenario), ui: *dir, reload: newBus(), events: newBus()}
	go s.watch()
	fmt.Printf("dashboard dev server (%s data)\n  http://%s/#token=%s\n", *scenario, ln.Addr(), token)
	log.Fatal(http.Serve(ln, s.routes()))
}

type server struct {
	world  *mockWorld
	ui     string
	reload *bus // UI file changes
	events *bus // mock-state changes, surfaced as the Works' event stream
}

// bus fans a message out to every subscriber without blocking the sender.
type bus struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func newBus() *bus { return &bus{subs: map[chan string]struct{}{}} }
func (b *bus) sub() chan string {
	c := make(chan string, 8)
	b.mu.Lock()
	b.subs[c] = struct{}{}
	b.mu.Unlock()
	return c
}
func (b *bus) unsub(c chan string) { b.mu.Lock(); delete(b.subs, c); b.mu.Unlock() }
func (b *bus) send(m string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.subs {
		select {
		case c <- m:
		default:
		}
	}
}

// watch polls the UI directory and tells the page what changed.
func (s *server) watch() {
	seen := map[string]time.Time{}
	first := true
	for range time.Tick(300 * time.Millisecond) {
		changed := ""
		entries, _ := os.ReadDir(s.ui)
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || e.IsDir() {
				continue
			}
			if t, ok := seen[e.Name()]; !first && (!ok || !t.Equal(info.ModTime())) {
				if changed == "" || !strings.HasSuffix(e.Name(), ".css") {
					changed = e.Name()
				}
			}
			seen[e.Name()] = info.ModTime()
		}
		first = false
		if changed != "" {
			s.reload.send(changed)
		}
	}
}

const reloadJS = `(() => {
  let opened = false;
  const es = new EventSource('/__dev/reload');
  es.onopen = () => { if (opened) location.reload(); opened = true; };
  es.onmessage = (e) => {
    if (e.data.endsWith('.css')) {
      for (const l of document.querySelectorAll('link[rel=stylesheet]')) l.href = l.href.split('?')[0] + '?t=' + Date.now();
    } else location.reload();
  };
})();`

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	files := http.FileServer(http.Dir(s.ui))
	mux.HandleFunc("GET /__dev/reload.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		fmt.Fprint(w, reloadJS)
	})
	mux.HandleFunc("GET /__dev/reload", func(w http.ResponseWriter, r *http.Request) {
		s.stream(w, r, s.reload, func(m string) string { return "data: " + m + "\n\n" })
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		page, err := os.ReadFile(filepath.Join(s.ui, "index.html"))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		page = bytes.Replace(page, []byte("</body>"), []byte(`<script src="/__dev/reload.js"></script></body>`), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(page)
	})
	mux.HandleFunc("GET /ui/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.StripPrefix("/ui/", files).ServeHTTP(w, r)
	})
	mux.Handle("/", s.api())
	return mux
}

// stream holds an SSE response open, writing one frame per bus message.
func (s *server) stream(w http.ResponseWriter, r *http.Request, b *bus, frame func(string) string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, ": ok\n\n")
	fl.Flush()
	c := b.sub()
	defer b.unsub(c)
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case m := <-c:
			fmt.Fprint(w, frame(m))
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
		case <-r.Context().Done():
			return
		}
		fl.Flush()
	}
}

func (s *server) api() http.Handler {
	mux := http.NewServeMux()
	wd := s.world
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, code int, msg string) { reply(w, code, map[string]string{"error": msg}) }
	var n int
	// read serves a GET under the lock; fn returns the body or an error message.
	read := func(pattern string, fn func(r *http.Request) (any, string)) {
		mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
			wd.mu.Lock()
			defer wd.mu.Unlock()
			v, e := fn(r)
			if e != "" {
				fail(w, 404, e)
				return
			}
			reply(w, 200, v)
		})
	}
	// act serves a command that edits the mock state, then announces the change.
	act := func(pattern string, fn func(r *http.Request, j *mockJob) string) {
		mux.HandleFunc("POST "+pattern, func(w http.ResponseWriter, r *http.Request) {
			wd.mu.Lock()
			var j *mockJob
			if id := r.PathValue("id"); id != "" && strings.HasPrefix(pattern, "/jobs/") {
				if j = wd.job(id); j == nil {
					wd.mu.Unlock()
					fail(w, 404, "not found")
					return
				}
			}
			msg := fn(r, j)
			wd.mu.Unlock()
			if msg != "" {
				fail(w, 409, msg)
				return
			}
			n++
			s.events.send(strconv.Itoa(n))
			reply(w, 200, map[string]any{"ok": true})
		})
	}

	read("/health", func(*http.Request) (any, string) {
		return map[string]int{"pid": os.Getpid(), "schema_version": store.SchemaVersion()}, ""
	})
	read("/board", func(*http.Request) (any, string) {
		out := []any{}
		for _, j := range wd.Jobs {
			out = append(out, j.board())
		}
		return out, ""
	})
	read("/usage", func(*http.Request) (any, string) { return wd.usage(), "" })
	read("/sites", func(*http.Request) (any, string) { return wd.Sites, "" })
	read("/catalogue", func(*http.Request) (any, string) { return wd.Catalogue, "" })
	read("/queue", func(*http.Request) (any, string) { return wd.Queue, "" })
	read("/needs-you", func(*http.Request) (any, string) { return wd.NeedsYou, "" })
	read("/asks", func(r *http.Request) (any, string) {
		out := []store.Ask{}
		for _, j := range wd.Jobs {
			out = append(out, j.Asks...)
		}
		return out, ""
	})
	withJob := func(pattern string, fn func(j *mockJob) any) {
		read(pattern, func(r *http.Request) (any, string) {
			j := wd.job(r.PathValue("id"))
			if j == nil {
				return nil, "not found"
			}
			return fn(j), ""
		})
	}
	withJob("/jobs/{id}", func(j *mockJob) any {
		asks := j.Asks
		if asks == nil {
			asks = []store.Ask{}
		}
		states := map[string]string{}
		if j.Plan != nil {
			states = works.StepStates(j.Plan.Steps, j.Dispatches)
		}
		blocked := false
		for _, a := range asks {
			blocked = blocked || (a.Holder == store.HolderArchwright && a.Blocking)
		}
		return struct {
			store.Job
			Plan         *store.Plan       `json:"plan"`
			Asks         []store.Ask       `json:"asks"`
			BlockedOnYou bool              `json:"blocked_on_you"`
			StepStates   map[string]string `json:"step_states"`
		}{j.Job, j.Plan, asks, blocked, states}
	})
	withJob("/jobs/{id}/plan", func(j *mockJob) any {
		if j.Plan == nil {
			return map[string]any{}
		}
		return j.Plan
	})
	withJob("/jobs/{id}/dispatches", func(j *mockJob) any { return orEmpty(j.Dispatches) })
	withJob("/jobs/{id}/runs", func(j *mockJob) any { return orEmpty(j.Runs) })
	withJob("/jobs/{id}/worktrees", func(j *mockJob) any { return orEmpty(j.Worktrees) })
	withJob("/jobs/{id}/reviews", func(j *mockJob) any {
		next := ""
		if len(j.Reviews) == 0 && j.Plan != nil {
			next = "none yet"
		}
		return map[string]any{"reviews": orEmpty(j.Reviews), "next": next}
	})
	read("/runs/{id}/events", func(r *http.Request) (any, string) { return orEmpty(wd.Events[r.PathValue("id")]), "" })
	read("/runs/{id}/events/{seq}/raw", func(r *http.Request) (any, string) {
		return map[string]any{"type": "assistant", "run": r.PathValue("id"), "seq": r.PathValue("seq"),
			"note": "mock vendor event", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": "…"}}}}, ""
	})
	read("/events", func(*http.Request) (any, string) { return map[string]any{"events": []any{}}, "" })

	// Commands that move the mock state.
	act("/jobs/{id}/plan/approve", func(_ *http.Request, j *mockJob) string {
		if j.Plan == nil || j.Plan.Approval != store.ApprovalPending {
			return "the Plan is not waiting for approval"
		}
		j.Plan.Approval = store.ApprovalApproved
		return ""
	})
	act("/jobs/{id}/plan/reject", func(_ *http.Request, j *mockJob) string {
		if j.Plan == nil || j.Plan.Approval != store.ApprovalPending {
			return "the Plan is not waiting for approval"
		}
		j.Plan.Approval = store.ApprovalRejected
		return ""
	})
	act("/jobs/{id}/pause", func(_ *http.Request, j *mockJob) string {
		j.Job.PausedAt = time.Now().UTC().Format(time.RFC3339)
		return ""
	})
	act("/jobs/{id}/resume", func(_ *http.Request, j *mockJob) string { j.Job.PausedAt = ""; return "" })
	act("/jobs/{id}/cancel", func(_ *http.Request, j *mockJob) string { j.Job.Outcome, j.Job.ParkedFor = "discarded", ""; return "" })
	act("/jobs/{id}/purge", func(_ *http.Request, j *mockJob) string {
		j.Job.PurgedAt = time.Now().UTC().Format(time.RFC3339)
		return ""
	})
	mux.HandleFunc("DELETE /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		wd.mu.Lock()
		for i, j := range wd.Jobs {
			if j.Job.ID == r.PathValue("id") {
				wd.Jobs = append(wd.Jobs[:i], wd.Jobs[i+1:]...)
				break
			}
		}
		wd.mu.Unlock()
		n++
		s.events.send(strconv.Itoa(n))
		reply(w, 200, map[string]any{"ok": true})
	})
	closeAsk := func(r *http.Request, discard bool) string {
		id := r.PathValue("id")
		for _, j := range wd.Jobs {
			for i, a := range j.Asks {
				if a.ID == id {
					j.Asks = append(j.Asks[:i], j.Asks[i+1:]...)
					if discard {
						j.Job.Outcome = "discarded"
					}
					wd.NeedsYou = filterItems(wd.NeedsYou, func(it store.NeedsYouItem) bool { return !(it.Kind == store.NeedsAsk && it.JobID == j.Job.ID) })
					return ""
				}
			}
		}
		return "not found"
	}
	act("/asks/{id}/answer", func(r *http.Request, _ *mockJob) string { return closeAsk(r, false) })
	act("/asks/{id}/accept-default", func(r *http.Request, _ *mockJob) string { return closeAsk(r, false) })
	act("/asks/{id}/skip", func(r *http.Request, _ *mockJob) string { return closeAsk(r, false) })
	act("/asks/{id}/cancel", func(r *http.Request, _ *mockJob) string { return closeAsk(r, true) })
	act("/catalogue/reload", func(*http.Request, *mockJob) string { return "" })

	// Judgment commands are queued for the Archwright and change nothing here.
	for _, p := range []string{"/instruct", "/jobs/{id}/instruct", "/jobs/{id}/retry", "/jobs/{id}/replan", "/jobs/{id}/change-jig"} {
		mux.HandleFunc("POST "+p, func(w http.ResponseWriter, r *http.Request) {
			reply(w, 202, map[string]any{"queued": true, "event_id": 0, "job_id": r.PathValue("id")})
		})
	}

	mux.HandleFunc("GET /events/stream", func(w http.ResponseWriter, r *http.Request) {
		s.stream(w, r, s.events, func(m string) string { return "id: " + m + "\nevent: mock\ndata: {}\n\n" })
	})

	// Every API call, as the real Works does, must carry the token.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			fail(w, 401, "unauthorized (the dev token is \""+token+"\")")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func filterItems(in []store.NeedsYouItem, keep func(store.NeedsYouItem) bool) []store.NeedsYouItem {
	out := in[:0:0]
	for _, it := range in {
		if keep(it) {
			out = append(out, it)
		}
	}
	return out
}
