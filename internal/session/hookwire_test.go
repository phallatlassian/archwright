package session

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "aw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func envOf(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

// newTestReceiver skips where the platform forbids unix sockets, saying so.
func newTestReceiver(t *testing.T) *Receiver {
	t.Helper()
	r, err := NewReceiver(filepath.Join(shortDir(t), "h"))
	if err != nil {
		t.Skipf("cannot listen on a unix socket here (%v): the hook receiver was NOT exercised", err)
	}
	t.Cleanup(r.Close)
	return r
}

func TestAHookReachesTheReceiverAndIsAlsoSpooled(t *testing.T) {
	r := newTestReceiver(t)
	var mu sync.Mutex
	var got []Delivery
	r.Register("run-1", "tok", func(d Delivery) { mu.Lock(); got = append(got, d); mu.Unlock() })
	file := filepath.Join(shortDir(t), "hooks.jsonl")
	env := envOf(map[string]string{envRun: "run-1", EnvHookToken: "tok", EnvHookSocket: r.Path(), EnvHookFile: file})
	if err := SendHook(env, strings.NewReader(`{"hook_event_name":"Stop","session_id":"s"}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Run != "run-1" || !strings.Contains(string(got[0].Payload), `"Stop"`) {
		t.Fatalf("delivered = %+v", got)
	}
	// The spool file is written first, whether or not the post lands.
	b, _ := os.ReadFile(file)
	if strings.Count(string(b), "\n") != 1 || !strings.Contains(string(b), got[0].ID) {
		t.Fatalf("spool = %q", b)
	}
}

func TestAHookWithNoReachableReceiverLeavesItInTheSpoolAndSucceeds(t *testing.T) {
	file := filepath.Join(shortDir(t), "hooks.jsonl")
	env := envOf(map[string]string{envRun: "run-1", EnvHookToken: "tok", EnvHookSocket: "/nonexistent/h.sock", EnvHookFile: file})
	if err := SendHook(env, strings.NewReader(`{"hook_event_name":"Stop"}`)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), `"Stop"`) {
		t.Fatalf("spool = %q", b)
	}
}

func TestTheReceiverRefusesAnUnknownRunOrTheWrongToken(t *testing.T) {
	r := newTestReceiver(t)
	delivered := 0
	r.Register("run-1", "tok", func(Delivery) { delivered++ })
	for _, e := range []map[string]string{
		{envRun: "run-2", EnvHookToken: "tok"},
		{envRun: "run-1", EnvHookToken: "wrong"},
	} {
		e[EnvHookSocket] = r.Path()
		SendHook(envOf(e), strings.NewReader(`{"hook_event_name":"Stop"}`))
	}
	if delivered != 0 {
		t.Fatalf("%d hooks delivered", delivered)
	}
}

func TestAHookThatIsNotJSONIsNotDelivered(t *testing.T) {
	file := filepath.Join(shortDir(t), "hooks.jsonl")
	if err := SendHook(envOf(map[string]string{EnvHookFile: file}), strings.NewReader("not json")); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a non-JSON payload was spooled")
	}
}

func TestTheReceiverSocketPathMustFitTheUnixLimit(t *testing.T) {
	dir := filepath.Join(shortDir(t), strings.Repeat("d", 100))
	if _, err := NewReceiver(dir); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("err = %v", err)
	}
}

func TestADeliveryArrivingOverBothPathsCountsOnce(t *testing.T) {
	c := newCore("tok")
	d := Delivery{ID: "h1", Token: "tok", Payload: []byte(`{"hook_event_name":"SessionStart","session_id":"s"}`)}
	c.deliver(d)
	c.deliver(d) // the spool copy of a hook the socket already delivered
	c.deliver(Delivery{ID: "h2", Token: "wrong", Payload: []byte(`{"hook_event_name":"UserPromptSubmit"}`)})
	if got := c.State(); got != StateIdle {
		t.Fatalf("state = %s", got)
	}
	n := 0
	c.finish(Result{})
	for range c.Events() {
		n++
	}
	if n != 2 { // idle, ended
		t.Fatalf("%d events", n)
	}
}
