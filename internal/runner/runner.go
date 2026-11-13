// Package runner is the contract between the Works and the stock vendor agent
// CLIs it spawns (ADR 0003): start, an event stream, cancel, result and
// capability flags, with one adapter per vendor and a fake for tests.
// Adapters carry the hard-coded never-list of ADR 0011 (guard.go).
package runner

import (
	"context"
	"encoding/json"
	"sync"
)

// Kind is a normalized event kind. The set is deliberately small; anything an
// adapter cannot map is KindOther, with the vendor event kept in Raw.
type Kind string

const (
	KindPosture          Kind = "posture"           // the run's permission posture; always the first event
	KindStarted          Kind = "started"           // the vendor session began
	KindMessage          Kind = "message"           // agent text
	KindToolUse          Kind = "tool_use"          // the agent invoked a tool
	KindToolResult       Kind = "tool_result"       // a tool's output
	KindPermissionDenied Kind = "permission_denied" // an action that would have prompted was denied (ADR 0011)
	KindResult           Kind = "result"            // the vendor's final message
	KindOther            Kind = "other"
)

// Event is one normalized event. Raw is the vendor event it came from, stored
// alongside (ADR 0003); events an adapter synthesizes itself have no Raw.
type Event struct {
	Seq       int             `json:"seq"`
	Kind      Kind            `json:"kind"`
	Text      string          `json:"text,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Command   string          `json:"command,omitempty"` // the exact command or input of a tool use or denial
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Posture   *Posture        `json:"posture,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// Posture is the permission posture a run started under, recorded as its
// first event (ADR 0011).
type Posture struct {
	Runner  string   `json:"runner"`
	Version string   `json:"version"`
	Mode    string   `json:"mode"`    // the permission mode requested
	Sandbox string   `json:"sandbox"` // always "inherited": the factory never changes it
	Flags   []string `json:"flags"`   // every flag passed, the brief excluded
	Notices []string `json:"notices"` // managed-policy and other notices seen
}

// Status is how a run ended.
type Status string

const (
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Denial is an action the runner denied because it would have prompted.
type Denial struct {
	Tool      string `json:"tool"`
	Command   string `json:"command"`
	ToolUseID string `json:"tool_use_id,omitempty"`
}

// Usage is what a run consumed, where the vendor reports it.
type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Result is how a run ended.
type Result struct {
	Status     Status          `json:"status"`
	Final      string          `json:"final,omitempty"` // the final message
	Structured json.RawMessage `json:"structured,omitempty"`
	Usage      Usage           `json:"usage"`
	Denials    []Denial        `json:"denials,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// Capabilities are the flags an adapter reports; a Guild may require some
// (ADR 0003).
type Capabilities struct {
	Resume           bool `json:"resume"`
	StructuredOutput bool `json:"structured_output"`
	Usage            bool `json:"usage"`
	PermissionDenied bool `json:"permission_denied"` // reports denials as events
}

// Limits bound one run.
type Limits struct {
	Model        string
	MaxTurns     int
	MaxBudgetUSD float64
}

// StartRequest is everything a run starts from.
type StartRequest struct {
	Brief  string
	Dir    string // the working directory
	Limits Limits
	Env    map[string]string // ARCHWRIGHT_RUN, ARCHWRIGHT_SPOOL, ... (ADR 0018)
	Spawn  Spawn
}

// Spawn is what a caller may ask of the run's permission posture. Everything
// in it is checked against the never-list; none of it can widen permissions.
type Spawn struct {
	Mode           string   // "", default, plan or dontAsk
	ExtraArgs      []string // extra runner flags
	Settings       string   // a --settings override
	MCPConfig      string   // an --mcp-config override
	AllowRules     []string // allow rules to add
	SettingsWrites []string // settings files to write
	DisableSandbox bool
}

// EffectiveMode is the permission mode the run will have.
func (s Spawn) EffectiveMode() string {
	if s.Mode == "" {
		return ModeDefault
	}
	return s.Mode
}

// SandboxInherited is the sandbox posture of every run: the factory never changes it.
const SandboxInherited = "inherited"

func newPosture(runner, version string, spawn Spawn, flags []string) Event {
	return Event{Kind: KindPosture, Posture: &Posture{
		Runner: runner, Version: version, Mode: spawn.EffectiveMode(), Sandbox: SandboxInherited,
		Flags: flags, Notices: []string{},
	}}
}

// Run is one started run. Its event channel must be drained or abandoned
// together with the process; a consumer that stops reading holds one goroutine.
type Run interface {
	// Events replays every event from the first, then follows the run, and is
	// closed once the run has ended. It may be called more than once.
	Events() <-chan Event
	// Cancel stops the run; Result then reports StatusCancelled.
	Cancel()
	// Result blocks until the run ends.
	Result() Result
}

// Runner starts runs on one vendor CLI.
type Runner interface {
	Name() string
	Capabilities() Capabilities
	// Start refuses a request the never-list forbids, with an error wrapping
	// ErrForbidden and before anything is spawned.
	Start(ctx context.Context, req StartRequest) (Run, error)
}

// log is an append-only event log with replaying subscribers, so a consumer
// that is slow or absent never stalls the runner.
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

// add appends an event, numbering it.
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

// subscribe returns a channel replaying the log, closed when it is.
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
