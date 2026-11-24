// Package session is the contract between the Works and the interactive
// Sessions it hosts (ADR 0026): launch a stock vendor CLI in a pane, follow
// its state through the vendor's hooks, and cancel it. It replaces the
// headless contract of package runner, which stays until it is removed; the
// never-list is that package's (ADR 0027), so it holds for both.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/phallatlassian/archwright/internal/runner"
)

// State is what a Session is doing, derived from hooks and pane liveness
// (docs/spec/tmux-runtime.md).
type State string

const (
	StateStarting            State = "starting" // launched; no SessionStart hook yet
	StateWorking             State = "working"
	StateWaitingOnPermission State = "waiting-on-permission" // a PermissionRequest is outstanding; a human answers it in the pane (ADR 0027)
	StateIdle                State = "idle"
	StateEnded               State = "ended"
)

// Capabilities are the flags an adapter reports; a Guild may require some
// (ADR 0003's rule, kept by ADR 0026).
type Capabilities struct {
	// Hooks: the Session's state comes from the vendor's hooks. Without them
	// the Works falls back to the pane and the transcript and marks state
	// unreliable (ADR 0027).
	Hooks bool `json:"hooks"`
	// PermissionRequests: the adapter reports a pending prompt as a state.
	PermissionRequests bool `json:"permission_requests"`
	// Resume: a Session can be resumed by id.
	Resume bool `json:"resume"`
	// Queue: a busy Session can be messaged, the text arriving as a new turn
	// once the current one has stopped (Codex's `codex queue`).
	Queue bool `json:"queue"`
}

// Limits bound one Session. An interactive CLI enforces no turn or budget
// cap, so the model is the only limit a launch can carry; a limit that cannot
// be enforced is not expressible rather than silently ignored.
type Limits struct {
	Model string
}

// LaunchRequest is everything a Session starts from.
type LaunchRequest struct {
	Run    string // the run id; labels the pane and routes the Session's hooks
	Job    string // names the Job's window in the factory session
	Role   string // archwright, warden or wright; labels the pane
	Brief  string // the first prompt
	Dir    string // the working directory: the Session's worktree
	Limits Limits
	Env    map[string]string // ARCHWRIGHT_RUN, ARCHWRIGHT_SPOOL, ... (ADR 0018)
	Spawn  runner.Spawn      // what the caller asks of the permission posture; checked against the never-list
}

// EventKind says what an Event reports.
type EventKind string

const (
	KindPosture EventKind = "posture" // the permission posture the Session started under; always the first event
	KindState   EventKind = "state"   // the Session's state changed
	KindNote    EventKind = "note"    // something the developer should know that is not a state change
)

// Event is one entry of a Session's log.
type Event struct {
	Seq     int             `json:"seq"`
	Kind    EventKind       `json:"kind"`
	State   State           `json:"state,omitempty"`
	Prev    State           `json:"prev,omitempty"`
	Hook    string          `json:"hook,omitempty"`    // the hook event behind a state change; empty for the pane's own end
	Tool    string          `json:"tool,omitempty"`    // the tool a permission request is for
	Command string          `json:"command,omitempty"` // the exact command or input of that tool
	Text    string          `json:"text,omitempty"`
	Posture *runner.Posture `json:"posture,omitempty"`
	Raw     json.RawMessage `json:"raw,omitempty"` // the hook payload behind the event
}

// Info is where a Session lives and what the vendor reported about itself.
// The pane id is canonical (ADR 0026); the rest arrives with the SessionStart hook.
type Info struct {
	Pane           string `json:"pane"`
	Window         string `json:"window"`
	SessionID      string `json:"session_id,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	// Turns is how many times the agent finished a turn (a Stop hook with no
	// permission outstanding): the first one means the brief has been worked on.
	Turns int `json:"turns"`
	// Prompts is how many prompts the agent has taken (UserPromptSubmit hooks,
	// a duplicate delivery counted once): a Nudge is confirmed by it growing.
	Prompts int `json:"prompts"`
}

// Status is how a Session ended.
type Status = runner.Status

const (
	StatusDone      = runner.StatusDone
	StatusFailed    = runner.StatusFailed
	StatusCancelled = runner.StatusCancelled
)

// Result is how a Session ended.
type Result struct {
	Status Status `json:"status"`
	// ExitStatus is the process's exit status, -1 when the pane was killed or its status never read.
	ExitStatus int    `json:"exit_status"`
	Error      string `json:"error,omitempty"`
}

// Session is one launched Session.
type Session interface {
	Info() Info
	State() State
	// Events replays every event from the first, then follows the Session, and
	// is closed once it has ended. It may be called more than once.
	Events() <-chan Event
	// Nudge delivers text to the Session as a new prompt, and returns once the
	// agent's UserPromptSubmit hook confirms it took it. It is permitted only
	// when the Session is idle with no PermissionRequest outstanding: in any
	// other state it types nothing and returns an error wrapping ErrNotIdle,
	// because Enter at a prompt approves it (docs/spec/tmux-runtime.md). A
	// delivery the hook never confirms returns ErrNudgeUnconfirmed.
	Nudge(text string) error
	// Cancel interrupts the Session and then kills its pane after a short
	// grace; Result then reports StatusCancelled. The worktree is untouched.
	Cancel()
	// Result blocks until the Session ends.
	Result() Result
}

// ErrNotIdle is why a Nudge was refused: the Session is not idle, or has a
// PermissionRequest outstanding. Nothing was sent to it.
var ErrNotIdle = errors.New("the Session is not idle")

// ErrNudgeUnconfirmed is why a Nudge's delivery is in doubt: the text was
// pasted and Enter sent, but no UserPromptSubmit hook reported it in time. The
// text may be waiting in the Session's composer, so it is not sent again.
var ErrNudgeUnconfirmed = errors.New("the Nudge was not confirmed by a UserPromptSubmit hook")

// ErrNotQueueable is why a Session could not be messaged while busy: it is not
// working, or the vendor has no queue.
var ErrNotQueueable = errors.New("the Session cannot be queued a message")

// Queuer is a Session a busy agent can be messaged through. A Runner whose
// Capabilities say Queue returns Sessions that implement it.
type Queuer interface {
	// Queue delivers text to a working Session as a new turn after its next
	// Stop. It refuses, wrapping ErrNotQueueable, in any other state: a prompt
	// is never typed at or answered (ADR 0027).
	Queue(text string) error
}

// Runner launches Sessions on one vendor CLI.
type Runner interface {
	Name() string
	Capabilities() Capabilities
	// Launch refuses a request the never-list forbids, with an error wrapping
	// runner.ErrForbidden and before anything is spawned.
	Launch(ctx context.Context, req LaunchRequest) (Session, error)
}

// log is an append-only event log with replaying subscribers, so a consumer
// that is slow or absent never stalls a Session.
type log struct {
	mu     sync.Mutex
	cond   *sync.Cond
	events []Event
	closed bool
}

func newLog() *log {
	l := &log{}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *log) add(e Event) {
	l.mu.Lock()
	e.Seq = len(l.events) + 1
	l.events = append(l.events, e)
	l.mu.Unlock()
	l.cond.Broadcast()
}

func (l *log) close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.cond.Broadcast()
}

// subscribe returns a channel replaying the log, closed when it is. The
// goroutine behind it ends when the log closes, so a consumer that stops
// reading holds one goroutine until then.
func (l *log) subscribe() <-chan Event {
	ch := make(chan Event)
	go func() {
		defer close(ch)
		for i := 0; ; i++ {
			l.mu.Lock()
			for i >= len(l.events) && !l.closed {
				l.cond.Wait()
			}
			if i >= len(l.events) {
				l.mu.Unlock()
				return
			}
			e := l.events[i]
			l.mu.Unlock()
			ch <- e
		}
	}()
	return ch
}

// oneLine is s's first non-empty line, trimmed.
func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
