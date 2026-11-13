package works

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

const (
	eventPage       = 500
	eventPoll       = 100 * time.Millisecond
	eventKeepalive  = 15 * time.Second
	defaultEventLim = 200
)

// cursorOf reads where a reader resumes: Last-Event-ID, as EventSource sends
// on reconnect, else the after query parameter. Zero starts from the first event.
func cursorOf(r *http.Request) (int64, error) {
	// A reconnecting EventSource keeps its URL, so the id it last saw wins over the one it first asked for.
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid cursor %q", raw)
	}
	return n, nil
}

// eventsPage is GET /events: one page of events after the cursor, for readers
// that do not stream.
func eventsPage(st *store.Store, reply func(http.ResponseWriter, int, any), fail func(http.ResponseWriter, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		after, err := cursorOf(r)
		if err != nil {
			fail(w, err)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > eventPage {
			limit = defaultEventLim
		}
		evs, err := st.EventsAfter(after, r.URL.Query().Get("job"), limit)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, evs)
	}
}

// eventsStream is GET /events/stream: server-sent events, one way. It replays
// everything after the cursor from the events table, then follows new events.
// Ids only grow and each page starts after the last id sent, so a reader that
// reconnects with the last id it saw misses nothing and sees nothing twice.
func eventsStream(st *store.Store, done <-chan struct{}, fail func(http.ResponseWriter, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		after, err := cursorOf(r)
		if err != nil {
			fail(w, err)
			return
		}
		job := r.URL.Query().Get("job")
		fl, ok := w.(http.Flusher)
		if !ok {
			fail(w, fmt.Errorf("streaming is not supported"))
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "retry: 2000\n\n")
		fl.Flush()

		poll := time.NewTicker(eventPoll)
		defer poll.Stop()
		quiet := time.Now()
		for {
			evs, err := st.EventsAfter(after, job, eventPage)
			if err != nil {
				return // the reader reconnects from its cursor
			}
			for _, e := range evs {
				data, _ := json.Marshal(e)
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, data)
				after = e.ID
			}
			if len(evs) > 0 {
				fl.Flush()
				quiet = time.Now()
				if len(evs) == eventPage {
					continue // more waiting
				}
			} else if time.Since(quiet) >= eventKeepalive {
				fmt.Fprint(w, ": keepalive\n\n")
				fl.Flush()
				quiet = time.Now()
			}
			select {
			case <-r.Context().Done():
				return
			case <-done:
				return
			case <-poll.C:
			}
		}
	}
}
