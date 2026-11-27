package works

import (
	"strings"
	"sync"
	"testing"
	"time"

	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

// focusFake is the fake Session runner with a scripted tmux client: how long it
// has been idle, and every focus that was asked for.
type focusFake struct {
	*hosted.Fake
	mu       sync.Mutex
	clientIn bool          // a client is attached
	idle     time.Duration // how long since it last had input
	focused  []string
	asked    int
}

func (f *focusFake) set(attached bool, idle time.Duration) {
	f.mu.Lock()
	f.clientIn, f.idle = attached, idle
	f.mu.Unlock()
}

func (f *focusFake) FocusIfIdle(pane string, idle time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	if !f.clientIn || f.idle < idle {
		return false, nil
	}
	f.focused = append(f.focused, pane)
	return true, nil
}

func (f *focusFake) focuses() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.focused...), f.asked
}

func stallItems(t *testing.T, st *store.Store) []store.NeedsYouItem {
	t.Helper()
	all, err := st.NeedsYou()
	if err != nil {
		t.Fatal(err)
	}
	var out []store.NeedsYouItem
	for _, it := range all {
		if it.Kind == store.NeedsLaunchStall {
			out = append(out, it)
		}
	}
	return out
}

// stalledRig starts a Session that sends its SessionStart hook only after start.
func stalledRig(t *testing.T, start time.Duration, clientIn bool, idle time.Duration) (*sessionRig, *focusFake) {
	t.Helper()
	script := hosted.Turn(callsTool("report_result", `{"status":"done","result":"x","evidence":["e"]}`))
	script.Steps = append([]hosted.Step{{Pause: start}}, script.Steps...)
	ff := &focusFake{clientIn: clientIn, idle: idle}
	g := newSessionRigWith(t, script, func(g *sessionRig, sr *SessionRunner) hosted.Runner {
		ff.Fake = g.fake
		sr.Stalled = AnnounceStall(g.st)
		sr.StallAfter = 100 * time.Millisecond
		sr.FocusIdle = 5 * time.Second
		return ff
	})
	return g, ff
}

func TestStalledLaunchFilesAnItemUntilTheSessionStarts(t *testing.T) {
	g, _ := stalledRig(t, 700*time.Millisecond, false, 0)
	g.send("p")
	var items []store.NeedsYouItem
	eventually(t, "the stall item", func() bool { items = stallItems(t, g.st); return len(items) == 1 })
	it := items[0]
	if it.JobID != g.job.ID {
		t.Fatalf("item = %+v", it)
	}
	for _, want := range []string{"claude", "wright", g.job.ID, "tmux -L archwright attach-session -t =factory:" + g.job.ID, "select-pane -t %fake1"} {
		if !strings.Contains(it.Summary, want) {
			t.Fatalf("summary lacks %q: %q", want, it.Summary)
		}
	}
	eventually(t, "the item to clear when the Session starts", func() bool { return len(stallItems(t, g.st)) == 0 })
	g.waitReported("p")
}

func TestSessionThatStartsPromptlyFilesNoStallItem(t *testing.T) {
	g, ff := stalledRig(t, 0, true, time.Hour)
	g.send("p")
	g.waitReported("p")
	if n := len(stallItems(t, g.st)); n != 0 {
		t.Fatalf("%d stall items", n)
	}
	if got, _ := ff.focuses(); len(got) != 0 {
		t.Fatalf("focused %v", got)
	}
}

func TestStalledPaneIsFocusedOnlyWhenTheClientIsIdle(t *testing.T) {
	for _, c := range []struct {
		name     string
		attached bool
		idle     time.Duration
		want     bool
	}{
		{"idle client", true, time.Minute, true},
		{"active client", true, time.Second, false},
		{"no client", false, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, ff := stalledRig(t, 800*time.Millisecond, c.attached, c.idle)
			g.send("p")
			eventually(t, "the stall item", func() bool { return len(stallItems(t, g.st)) == 1 })
			eventually(t, "focus to be tried", func() bool { _, asked := ff.focuses(); return asked > 0 })
			got, _ := ff.focuses()
			if c.want != (len(got) == 1) || len(got) > 1 {
				t.Fatalf("focused %v, want focus = %v", got, c.want)
			}
			if c.want && got[0] != "%fake1" {
				t.Fatalf("focused %v", got)
			}
			if len(stallItems(t, g.st)) != 1 {
				t.Fatal("the item is there whether or not focus moved")
			}
			g.waitReported("p")
		})
	}
}

func TestFocusMovesWhenTheClientGoesIdleWhileStalled(t *testing.T) {
	g, ff := stalledRig(t, 2*time.Second, true, time.Second)
	g.send("p")
	eventually(t, "focus to be tried", func() bool { _, asked := ff.focuses(); return asked > 0 })
	if got, _ := ff.focuses(); len(got) != 0 {
		t.Fatalf("focused %v while the client was active", got)
	}
	ff.set(true, time.Minute)
	eventually(t, "focus", func() bool { got, _ := ff.focuses(); return len(got) == 1 })
	time.Sleep(600 * time.Millisecond)
	if got, _ := ff.focuses(); len(got) != 1 {
		t.Fatalf("focused again: %v", got)
	}
	g.waitReported("p")
}
