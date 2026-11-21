package works

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phallatlassian/archwright/internal/store"
)

const (
	// eventLogMax is the size at which events.log rolls to events.log.1.
	eventLogMax = 5 << 20
	// eventLogEvery is how often the tee looks for new events.
	eventLogEvery = 500 * time.Millisecond
	// eventLogSummaryMax is the longest summary, in runes, before an ellipsis.
	eventLogSummaryMax = 200
	eventLogBatch      = 500
)

// FormatEvent turns one event into one log line: UTC timestamp, job id ("-"
// without one), type and a short summary built from the payload alone, so the
// log carries only the summary tier of ADR 0017.
func FormatEvent(e store.Event) string {
	job := e.JobID
	if job == "" {
		job = "-"
	}
	line := e.At.UTC().Format(time.RFC3339) + " " + job + " " + e.Type
	if s := eventSummary(e); s != "" {
		line += " " + s
	}
	return line
}

func eventSummary(e store.Event) string {
	var p map[string]any
	if json.Unmarshal(e.Payload, &p) != nil {
		return ""
	}
	str := func(k string) string {
		switch v := p[k].(type) {
		case string:
			return v
		case float64, bool:
			return fmt.Sprint(v)
		}
		return ""
	}
	var s string
	switch {
	case strings.HasPrefix(e.Type, "needs_you."):
		s = str("summary")
	case e.Type == "job.created":
		s = str("title")
	case e.Type == "step.failed":
		s = join(str("step")+":", str("reason"))
	case strings.HasPrefix(e.Type, "step."):
		s = join(str("step"), str("guild"))
	case e.Type == "run.started":
		s = join(str("run"), "guild="+str("guild"), "runner="+str("runner"))
	case e.Type == "run.ended":
		s = join(str("run"), str("status"))
	default:
		s = keyValues(p)
	}
	return compact(s)
}

func join(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" && p != "guild=" && p != "runner=" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

// keyValues renders a payload's top-level string, number and bool fields as
// key=value pairs, so event types the formatter does not know still show up.
func keyValues(p map[string]any) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		switch v := p[k].(type) {
		case string:
			if v != "" {
				parts = append(parts, k+"="+v)
			}
		case float64, bool:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, " ")
}

// compact collapses whitespace (newlines included) and truncates to the summary limit.
func compact(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > eventLogSummaryMax {
		s = string(r[:eventLogSummaryMax]) + "…"
	}
	return s
}

// eventLog appends lines to a file capped at max bytes, rolling it to path+".1".
type eventLog struct {
	path string
	max  int64
	f    *os.File
	size int64
}

func (l *eventLog) open() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

func (l *eventLog) write(line string) error {
	line += "\n"
	if l.f != nil && l.size > 0 && l.size+int64(len(line)) > l.max {
		l.f.Close()
		l.f = nil
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			return err
		}
	}
	if l.f == nil {
		if err := l.open(); err != nil {
			return err
		}
	}
	n, err := l.f.WriteString(line)
	l.size += int64(n)
	return err
}

func (l *eventLog) close() {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// teeEvents appends each event committed after cursor (the newest event when
// the Works started) to the log at path until ctx ends. The events table stays the record; a failure to
// write goes to works.log and never reaches a Job.
func teeEvents(ctx context.Context, st *store.Store, cursor int64, path string, max int64, every time.Duration) {
	l := &eventLog{path: path, max: max}
	defer l.close()
	tick := time.NewTicker(every)
	defer tick.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		for {
			evs, err := st.EventsAfter(cursor, "", eventLogBatch)
			if err != nil {
				log.Printf("events.log: %v", err)
				break
			}
			for _, e := range evs {
				if err := l.write(FormatEvent(e)); err != nil {
					if !failing {
						log.Printf("events.log: %v", err)
					}
					failing = true
				} else {
					failing = false
				}
				cursor = e.ID // a line that cannot be written is dropped, not retried forever
			}
			if len(evs) < eventLogBatch {
				break
			}
		}
	}
}
