package session

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// core is what every Session shares: the state machine fed by hooks, the
// event log, and the one-shot result. An adapter adds how the Session is
// hosted (a pane, a script) and calls hook, note and finish.
type core struct {
	token string

	mu       sync.Mutex
	m        *Machine
	log      *log
	pane     Info // the adapter's part of Info: where the Session lives
	ids      map[string]bool
	done     chan struct{}
	result   Result
	finished bool
}

func newCore(token string) *core {
	return &core{token: token, m: NewMachine(), log: newLog(), ids: map[string]bool{}, done: make(chan struct{})}
}

// Info implements Session.
func (c *core) Info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.infoLocked()
}

func (c *core) infoLocked() Info {
	i := c.m.Info()
	i.Pane, i.Window = c.pane.Pane, c.pane.Window
	return i
}

func (c *core) setPane(pane, window string) {
	c.mu.Lock()
	c.pane.Pane, c.pane.Window = pane, window
	c.mu.Unlock()
}

// State implements Session.
func (c *core) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m.State()
}

// Events implements Session.
func (c *core) Events() <-chan Event { return c.log.subscribe() }

// Result implements Session.
func (c *core) Result() Result {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

// nudgeable says whether the Session may be Nudged now; see Session.Nudge.
func (c *core) nudgeable() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m.Nudgeable()
}

// prompts is how many prompts the agent has taken so far.
func (c *core) prompts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m.Info().Prompts
}

// awaitPrompt waits for the agent to take a prompt beyond the before-th: the
// UserPromptSubmit hook that confirms a Nudge. A Session that ends first, or
// stays silent past timeout, did not confirm it.
func (c *core) awaitPrompt(before int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if c.prompts() > before {
			return nil
		}
		select {
		case <-c.done:
			return fmt.Errorf("%w: the Session ended first", ErrNudgeUnconfirmed)
		case <-deadline.C:
			return fmt.Errorf("%w: none within %s", ErrNudgeUnconfirmed, timeout)
		case <-tick.C:
		}
	}
}

func (c *core) add(e Event) { c.log.add(e) }

// deliver takes one hook delivery, from the socket or the spool file. A
// delivery with the wrong token, or one already seen, is dropped.
func (c *core) deliver(d Delivery) {
	if d.Token != c.token {
		return
	}
	var h Hook
	if err := json.Unmarshal(d.Payload, &h); err != nil || h.Event == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ids[d.ID] || c.finished {
		return
	}
	c.ids[d.ID] = true
	c.applyLocked(h, d.Payload)
}

// feed applies a hook an adapter synthesises itself, with no delivery.
func (c *core) feed(h Hook) {
	raw, _ := json.Marshal(h)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished {
		c.applyLocked(h, raw)
	}
}

func (c *core) applyLocked(h Hook, raw json.RawMessage) {
	ch, ok := c.m.Apply(h)
	if !ok {
		return
	}
	// A SessionEnd ends the state; the result waits for the pane, whose adapter calls finish.
	c.log.add(Event{Kind: KindState, State: ch.State, Prev: ch.Prev, Hook: ch.Hook, Tool: ch.Tool, Command: ch.Command, Raw: raw})
}

// note logs something the developer should know that is not a state change.
func (c *core) note(text string) { c.log.add(Event{Kind: KindNote, Text: text}) }

// finish ends the Session with res, once. A Session whose state is not yet
// ended is ended by the pane or the cancel, not a hook, so the change is
// logged here with no hook behind it.
func (c *core) finish(res Result) {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	c.finished = true
	if c.m.State() != StateEnded {
		ch := c.m.End("")
		c.log.add(Event{Kind: KindState, State: ch.State, Prev: ch.Prev})
	}
	c.result = res
	c.mu.Unlock()
	c.log.close()
	close(c.done)
}
