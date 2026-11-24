package session

import (
	"encoding/json"
	"testing"
)

func hook(event string) Hook { return Hook{Event: event, SessionID: "s1"} }

func tool(event, name, cmd, useID string) Hook {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return Hook{Event: event, SessionID: "s1", ToolName: name, ToolInput: in, ToolUseID: useID}
}

// feed applies hooks in order and returns the state after each.
func feed(m *Machine, hooks ...Hook) []State {
	var got []State
	for _, h := range hooks {
		m.Apply(h)
		got = append(got, m.State())
	}
	return got
}

func wantStates(t *testing.T, got []State, want ...State) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("states = %v, want %v", got, want)
		}
	}
}

func TestSessionStartMovesStartingToIdleAndRecordsTheSession(t *testing.T) {
	m := NewMachine()
	if m.State() != StateStarting {
		t.Fatalf("initial state %s", m.State())
	}
	h := hook(HookSessionStart)
	h.TranscriptPath = "/t/s1.jsonl"
	if ch, ok := m.Apply(h); !ok || ch.Prev != StateStarting || ch.State != StateIdle {
		t.Fatalf("change = %+v %v", ch, ok)
	}
	if i := m.Info(); i.SessionID != "s1" || i.TranscriptPath != "/t/s1.jsonl" {
		t.Fatalf("info = %+v", i)
	}
	// A resume or clear sends SessionStart again mid-session; it changes nothing.
	m.Apply(hook(HookUserPromptSubmit))
	again := hook(HookSessionStart)
	again.SessionID = "s2"
	if _, ok := m.Apply(again); ok || m.State() != StateWorking {
		t.Fatalf("a later SessionStart moved the state to %s", m.State())
	}
}

func TestATurnGoesIdleWorkingIdleAndCountsStops(t *testing.T) {
	m := NewMachine()
	wantStates(t, feed(m, hook(HookSessionStart), hook(HookUserPromptSubmit), tool(HookPreToolUse, "Bash", "ls", "t1"), tool(HookPostToolUse, "Bash", "ls", "t1"), hook(HookStop)),
		StateIdle, StateWorking, StateWorking, StateWorking, StateIdle)
	if m.Info().Turns != 1 {
		t.Fatalf("turns = %d", m.Info().Turns)
	}
}

func TestPermissionRequestWaitsUntilItsToolUseCompletesEvenThroughAStop(t *testing.T) {
	m := NewMachine()
	feed(m, hook(HookSessionStart), hook(HookUserPromptSubmit))
	ch, _ := m.Apply(tool(HookPermissionReq, "Bash", "rm -rf build", "t1"))
	if ch.State != StateWaitingOnPermission || ch.Tool != "Bash" || ch.Command != "rm -rf build" {
		t.Fatalf("change = %+v", ch)
	}
	// Codex fires Stop while an approval is pending; that is not idle.
	wantStates(t, feed(m, hook(HookStop)), StateWaitingOnPermission)
	if m.Info().Turns != 0 {
		t.Fatalf("a Stop during a prompt counted as a turn")
	}
	wantStates(t, feed(m, tool(HookPostToolUse, "Bash", "rm -rf build", "t1"), hook(HookStop)), StateWorking, StateIdle)
}

func TestPermissionRequestWithoutAToolUseIDIsMatchedByToolAndInput(t *testing.T) {
	m := NewMachine()
	feed(m, hook(HookSessionStart), hook(HookUserPromptSubmit), tool(HookPermissionReq, "Bash", "make", ""))
	wantStates(t, feed(m, tool(HookPostToolUse, "Bash", "other", "t9")), StateWaitingOnPermission)
	wantStates(t, feed(m, tool(HookPostToolUse, "Bash", "make", "t2")), StateWorking)
}

func TestSessionWaitsWhileAnyPermissionRequestIsOutstanding(t *testing.T) {
	m := NewMachine()
	feed(m, hook(HookSessionStart), hook(HookUserPromptSubmit))
	wantStates(t, feed(m,
		tool(HookPermissionReq, "Bash", "a", "t1"), tool(HookPermissionReq, "Edit", "b", "t2"),
		tool(HookPostToolUse, "Bash", "a", "t1"), hook(HookStop),
		tool(HookPostToolUse, "Edit", "b", "t2"),
	), StateWaitingOnPermission, StateWaitingOnPermission, StateWaitingOnPermission, StateWaitingOnPermission, StateWorking)
}

func TestADuplicatedPermissionRequestIsOnePrompt(t *testing.T) {
	m := NewMachine()
	feed(m, hook(HookSessionStart), hook(HookUserPromptSubmit))
	req := tool(HookPermissionReq, "Bash", "x", "t1")
	req.TurnID = "turn-1"
	m.Apply(req)
	m.Apply(req) // delivered twice
	// One PostToolUse clears it only if the duplicate was not counted.
	post := tool(HookPostToolUse, "Bash", "x", "t1")
	post.TurnID = "turn-1"
	wantStates(t, feed(m, post), StateWorking)
}

func TestDuplicateSessionStartAndPromptDeliveriesAreDroppedButTwoStopsAreNot(t *testing.T) {
	m := NewMachine()
	start, prompt := hook(HookSessionStart), hook(HookUserPromptSubmit)
	prompt.TurnID = "turn-1"
	if _, ok := m.Apply(start); !ok {
		t.Fatal("first SessionStart ignored")
	}
	if _, ok := m.Apply(start); ok {
		t.Fatal("duplicate SessionStart applied")
	}
	m.Apply(prompt)
	m.Apply(tool(HookPermissionReq, "Bash", "x", "t1"))
	if _, ok := m.Apply(prompt); ok { // a duplicate delivery of the prompt: not a new turn, and it must not clear the prompt
		t.Fatal("duplicate UserPromptSubmit applied")
	}
	if m.State() != StateWaitingOnPermission {
		t.Fatalf("state = %s", m.State())
	}
	// Codex sends a second Stop in the same turn once the approval is answered.
	stop := hook(HookStop)
	stop.TurnID = "turn-1"
	m.Apply(stop)
	m.Apply(tool(HookPostToolUse, "Bash", "x", "t1"))
	wantStates(t, feed(m, stop), StateIdle)
}

func TestNotificationNeverChangesState(t *testing.T) {
	m := NewMachine()
	feed(m, hook(HookSessionStart), hook(HookUserPromptSubmit), tool(HookPermissionReq, "Bash", "x", "t1"))
	wantStates(t, feed(m, hook(HookNotification)), StateWaitingOnPermission)
	m2 := NewMachine()
	wantStates(t, feed(m2, hook(HookNotification)), StateStarting)
}

func TestANewPromptEndsAPermissionRequestThatWasNeverAnswered(t *testing.T) {
	m := NewMachine()
	feed(m, hook(HookSessionStart), hook(HookUserPromptSubmit), tool(HookPermissionReq, "Bash", "x", "t1")) // denied: no PostToolUse follows
	wantStates(t, feed(m, hook(HookUserPromptSubmit), hook(HookStop)), StateWorking, StateIdle)
}

func TestSessionEndEndsItAndLaterHooksAreIgnored(t *testing.T) {
	m := NewMachine()
	feed(m, hook(HookSessionStart), hook(HookUserPromptSubmit))
	wantStates(t, feed(m, hook(HookSessionEnd), hook(HookUserPromptSubmit), hook(HookStop)), StateEnded, StateEnded, StateEnded)
}

func TestASecondToolStartingDoesNotAnswerAPromptStillOpen(t *testing.T) {
	m := NewMachine()
	feed(m, hook(HookSessionStart), hook(HookUserPromptSubmit), tool(HookPermissionReq, "Bash", "a", "t1"))
	wantStates(t, feed(m, tool(HookPreToolUse, "Edit", "b", "t2"), hook(HookStop)), StateWaitingOnPermission, StateWaitingOnPermission)
	wantStates(t, feed(m, tool(HookPostToolUse, "Bash", "a", "t1")), StateWorking)
}
