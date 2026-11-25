package works

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/phallatlassian/archwright/internal/launch"
)

// Waking an idle Archwright (ADR 0001, 0020): every commit that files an item
// in the inbox posts a fixed nudge to the session's inbox socket, which starts
// a turn in an idle Claude Code session and reaches a running one between tool
// calls. The nudge never carries item content; the inbox stays the source of
// truth. It also keeps the unread marker the PostToolUse hook checks.
//
// This is the wake of an Archwright run in the developer's own terminal. An
// Archwright the Works hosts as a Session is Nudged instead
// (archwrightsession.go), and inboxChanged only asks its wake loop to look.

// nudgeTimeout bounds one post to a session's socket.
const nudgeTimeout = 5 * time.Second

// session is where an Archwright run's interactive session listens.
type session struct{ socket, token string }

func (ru *run) setSession(socket, token string) {
	ru.sessMu.Lock()
	ru.sess = &session{socket, token}
	ru.sessMu.Unlock()
}

func (ru *run) session() *session {
	ru.sessMu.Lock()
	defer ru.sessMu.Unlock()
	return ru.sess
}

// inboxChanged is the store's notifier: an item was filed. It refreshes the
// unread marker, then nudges every Archwright session, without waiting for it.
func (r *Runs) inboxChanged() {
	if a := r.archwrightSession(); a != nil {
		a.wake() // a hosted Archwright is Nudged, not sent to a socket
	}
	for _, ru := range r.archwrightRuns() {
		r.syncUnread(ru)
		if s := ru.session(); s != nil && r.startNudge() {
			go func() {
				defer r.nudges.Done()
				if err := postNudge(*s); err != nil {
					log.Printf("nudging the Archwright session: %v", err)
					// works.log is not always where the developer looks, so the
					// failure is also an event, which events.log tees.
					if err := r.st.AppendEvent("", "nudge.failed", map[string]string{"error": err.Error()}); err != nil {
						log.Printf("recording the failed nudge: %v", err)
					}
				}
			}()
		}
	}
}

// startNudge counts a post in flight, unless the Works is closing: Close waits
// for the posts, so none may start after it begins.
func (r *Runs) startNudge() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bg.Err() != nil {
		return false
	}
	r.nudges.Add(1)
	return true
}

func (r *Runs) archwrightRuns() []*run {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*run
	for _, ru := range r.runs {
		if ru.role == RoleArchwright {
			out = append(out, ru)
		}
	}
	return out
}

// syncUnread makes the run's unread marker match the inbox: present while an
// item is unread. A failure leaves the hooks and the nudge to deliver.
func (r *Runs) syncUnread(ru *run) {
	if ru == nil || ru.role != RoleArchwright {
		return
	}
	ru.markMu.Lock()
	defer ru.markMu.Unlock()
	unread, err := r.st.Inbox(true)
	if err != nil {
		log.Printf("the inbox's unread marker: %v", err)
		return
	}
	path := filepath.Join(ru.dir, launch.UnreadMarker)
	if len(unread) == 0 {
		os.Remove(path)
		return
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil && !os.IsNotExist(err) {
		log.Printf("the inbox's unread marker: %v", err)
	}
}

// postNudge posts the nudge to the session's socket: the auth line, then one
// NDJSON user message. The message shape is undocumented (anthropics/claude-code
// #103048); this one was verified against Claude Code 2.1.285. A bare text line
// is accepted by the socket but never delivered.
func postNudge(s session) error {
	conn, err := net.DialTimeout("unix", s.socket, nudgeTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(nudgeTimeout)); err != nil {
		return err
	}
	auth, err := json.Marshal(struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}{"auth", s.token})
	if err != nil {
		return err
	}
	msg, err := json.Marshal(struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}{Type: "user", Message: struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{"user", launch.Nudge}})
	if err != nil {
		return err
	}
	_, err = conn.Write([]byte(string(auth) + "\n" + string(msg) + "\n"))
	return err
}
