// Package session is the contract between the Works and the interactive
// Sessions it hosts (ADR 0026): launch a stock vendor CLI in a pane, follow
// its state through the vendor's hooks, and cancel it. The never-list it
// launches under is package runner's (ADR 0027).
package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

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
	// Resume: a Session can be resumed by id (LaunchRequest.Resume).
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
	// Resume, when set, is the vendor session id of an earlier Session of this
	// worktree to continue instead of starting a new one; Brief is then what the
	// resumed Session is told first. A Session that cannot be resumed (the
	// vendor no longer has it) ends without starting, as any Session that
	// fails to start does.
	Resume string
	// StartTimeout, when set, replaces the adapter's for this Session: how long
	// it may take to send its first SessionStart hook. A trust probe waits on a
	// person answering a dialog, which takes longer than an agent starting.
	StartTimeout time.Duration
	Env          map[string]string // ARCHWRIGHT_RUN, ARCHWRIGHT_SPOOL, ... (ADR 0018)
	Spawn        runner.Spawn      // what the caller asks of the permission posture; checked against the never-list
}

// EventKind says what an Event reports.
type EventKind string

const (
	KindPosture EventKind = "posture" // the permission posture the Session started under; always the first event
	KindState   EventKind = "state"   // the Session's state changed
	KindNote    EventKind = "note"    // something the developer should know that is not a state change
	// KindHuman: a prompt the Works did not send, typed by a person in the pane.
	// It is recorded and never blocked (ADR 0029).
	KindHuman EventKind = "human"
)

// Event is one entry of a Session's log.
type Event struct {
	Seq     int       `json:"seq"`
	Kind    EventKind `json:"kind"`
	State   State     `json:"state,omitempty"`
	Prev    State     `json:"prev,omitempty"`
	Hook    string    `json:"hook,omitempty"`    // the hook event behind a state change; empty for the pane's own end
	Tool    string    `json:"tool,omitempty"`    // the tool a permission request is for
	Command string    `json:"command,omitempty"` // the exact command or input of that tool
	// Turns is, on a state change, how many turns the Session had finished by then.
	Turns int `json:"turns,omitempty"`
	// Text is a note's text, a human prompt's description, and for the state
	// change a Stop reports the agent's last message of the turn.
	Text    string          `json:"text,omitempty"`
	Posture *runner.Posture `json:"posture,omitempty"`
	Raw     json.RawMessage `json:"raw,omitempty"` // the hook payload behind the event
}

// Info is where a Session lives and what the vendor reported about itself.
// The pane id is canonical (ADR 0026); the rest arrives with the SessionStart hook.
type Info struct {
	Pane   string `json:"pane"`
	Window string `json:"window"`
	// Dir is the Session's working directory, and Runner the vendor CLI it runs:
	// what the Works needs to find or resume it after a restart.
	Dir            string `json:"dir,omitempty"`
	Runner         string `json:"runner,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	// Turns is how many times the agent finished a turn (a Stop hook with no
	// permission outstanding): the first one means the brief has been worked on.
	Turns int `json:"turns"`
	// Prompts is how many prompts the agent has taken (UserPromptSubmit hooks,
	// a duplicate delivery counted once): a Nudge is confirmed by it growing.
	Prompts int `json:"prompts"`
	// Humans is how many of those prompts the Works did not send: a person typed them.
	Humans int `json:"humans"`
	// LastMessage is what the agent said at the end of its latest turn (a Stop
	// hook's last assistant message), kept so a Session that ends without
	// reporting still has its last words.
	LastMessage string `json:"last_message,omitempty"`
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

// ErrNoPane is why a Session could not be taken over: its pane is gone, or is
// no longer the Session's (tmux was restarted and the id reused, or the
// process ended and left a dead pane).
var ErrNoPane = errors.New("the Session's pane is gone")

// Adopter is a Runner that can take over a Session an earlier Works left
// running in its pane (ADR 0029). The pane id is what the Works recorded and is
// canonical; runs are every run the Works recorded against that pane, and the
// pane must have been launched by one of them, so a reused pane id never
// adopts a stranger. The Session is brought up to date from its hook spool.
// It returns an error wrapping ErrNoPane when there is nothing to adopt.
type Adopter interface {
	Adopt(pane string, runs ...string) (Session, error)
}

// Orphan is a live Session pane no run the Works recorded accounts for.
type Orphan struct {
	Pane, Run, Role string
}

// Orphaner is a Runner that can find the live Session panes the Works has no
// record of, and mark them. Orphans are flagged and never killed (ADR 0029).
type Orphaner interface {
	// FlagOrphans marks each live Session pane not in known (by pane id) as an
	// orphan and returns the ones it had not already flagged.
	FlagOrphans(known map[string]bool) ([]Orphan, error)
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
