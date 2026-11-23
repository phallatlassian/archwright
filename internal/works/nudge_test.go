package works

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/launch"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

// fakeSession stands in for a Claude Code session's inbox socket: it records
// each connection's lines.
type fakeSession struct {
	path  string
	lines chan []string
}

func newFakeSession(t *testing.T) *fakeSession {
	t.Helper()
	dir, err := os.MkdirTemp("", "aw") // a unix socket path is short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	fs := &fakeSession{path: ln.Addr().String(), lines: make(chan []string, 16)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var got []string
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					got = append(got, sc.Text())
				}
				fs.lines <- got
			}()
		}
	}()
	return fs
}

func (f *fakeSession) next(t *testing.T) []string {
	t.Helper()
	select {
	case l := <-f.lines:
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("the session got no nudge")
		return nil
	}
}

func (f *fakeSession) none(t *testing.T) {
	t.Helper()
	select {
	case l := <-f.lines:
		t.Fatalf("an unexpected nudge: %q", l)
	case <-time.After(200 * time.Millisecond):
	}
}

func register(t *testing.T, env RunEnv, socket string) {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"socket": socket, "token": "sock-token"})
	if resp := call(t, env, "reg-"+spool.NewID(), "register_session", string(in)); !resp.OK {
		t.Fatalf("register_session: %+v", resp.Error)
	}
}

func TestAnInboxInsertPostsOneNudgeWithNoItemContent(t *testing.T) {
	r, st := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	sess := newFakeSession(t)
	register(t, env, sess.path)

	if _, err := st.QueueCommand("instruct", "", "SECRET dashboard words", nil); err != nil {
		t.Fatal(err)
	}
	got := sess.next(t)
	if len(got) != 2 || got[0] != `{"type":"auth","token":"sock-token"}` || got[1] != launch.Nudge {
		t.Fatalf("lines on the socket: %q", got)
	}
	if strings.Contains(strings.Join(got, "\n"), "SECRET") {
		t.Fatalf("the nudge carried item content: %q", got)
	}
	sess.none(t)
}

func TestEachInboxInsertNudgesOnce(t *testing.T) {
	r, st := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	sess := newFakeSession(t)
	register(t, env, sess.path)
	for i := 0; i < 3; i++ {
		if _, err := st.QueueCommand("instruct", "", "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		sess.next(t)
	}
	sess.none(t)
}

func TestASocketErrorNeverFailsTheInsert(t *testing.T) {
	r, st := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	register(t, env, filepath.Join(os.TempDir(), "aw-no-such-socket"))
	if _, err := st.QueueCommand("instruct", "", "x", nil); err != nil {
		t.Fatalf("the insert failed on a dead socket: %v", err)
	}
	if unread, _ := st.Inbox(true); len(unread) != 1 {
		t.Fatalf("the item must be filed: %+v", unread)
	}
}

func TestNoSessionNoNudgeAndAnEndedRunClearsIt(t *testing.T) {
	r, st := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	sess := newFakeSession(t)
	if _, err := st.QueueCommand("instruct", "", "x", nil); err != nil { // nothing registered yet
		t.Fatal(err)
	}
	sess.none(t)
	register(t, env, sess.path)
	if err := r.End(env.Run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.QueueCommand("instruct", "", "y", nil); err != nil {
		t.Fatal(err)
	}
	sess.none(t)
}

func TestOnlyTheArchwrightRegistersASessionAndWithAnAbsoluteSocket(t *testing.T) {
	r, _ := newRuns(t)
	arch, warden := startRun(t, r, RoleArchwright), startRun(t, r, RoleWarden)
	if resp := call(t, warden, "w1", "register_session", `{"socket":"/tmp/x.sock","token":"t"}`); resp.OK || resp.Error.Code != "forbidden" {
		t.Fatalf("a Warden registered a session: %+v", resp)
	}
	for i, in := range []string{`{"socket":"relative.sock","token":"t"}`, `{"socket":"","token":"t"}`, `{"socket":"/tmp/x.sock","extra":1}`} {
		if resp := call(t, arch, "a"+string(rune('a'+i)), "register_session", in); resp.OK {
			t.Fatalf("%s was accepted", in)
		}
	}
}

func TestTheUnreadMarkerFollowsTheInbox(t *testing.T) {
	r, st := newRuns(t)
	if _, err := st.QueueCommand("instruct", "", "before the session", nil); err != nil {
		t.Fatal(err)
	}
	env := startRun(t, r, RoleArchwright)
	if !launch.HasUnread(env.Spool) {
		t.Fatal("items filed before the run started must show as unread")
	}
	if resp := call(t, env, "r1", "read_inbox", `{}`); !resp.OK {
		t.Fatal(resp.Error)
	}
	if launch.HasUnread(env.Spool) {
		t.Fatal("an emptied inbox must clear the marker")
	}
	if _, err := st.QueueCommand("instruct", "", "later", nil); err != nil {
		t.Fatal(err)
	}
	if !launch.HasUnread(env.Spool) {
		t.Fatal("a new item must set the marker")
	}
	if resp := call(t, env, "r2", "read_inbox", `{"peek":true}`); !resp.OK || !launch.HasUnread(env.Spool) {
		t.Fatal("peeking reads nothing, so the marker stays")
	}
}

func TestReadInboxDeliversNoMoreThanMaxCharsAndLeavesTheRestUnread(t *testing.T) {
	r, st := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	for _, s := range []string{strings.Repeat("a", 60), strings.Repeat("b", 60), strings.Repeat("c", 60)} {
		if _, err := st.QueueCommand("instruct", "", s, nil); err != nil {
			t.Fatal(err)
		}
	}
	read := func(id string) []store.InboxItem {
		resp := call(t, env, id, "read_inbox", `{"max_chars":150}`)
		var out InboxRead
		if !resp.OK || json.Unmarshal(resp.Output, &out) != nil {
			t.Fatalf("%+v", resp)
		}
		return out.Items
	}
	if got := read("m1"); len(got) != 2 || !strings.HasPrefix(got[0].Summary, "a") || !strings.HasPrefix(got[1].Summary, "b") {
		t.Fatalf("first read: %+v", got)
	}
	if !launch.HasUnread(env.Spool) {
		t.Fatal("the overflow is still unread")
	}
	if got := read("m2"); len(got) != 1 || !strings.HasPrefix(got[0].Summary, "c") {
		t.Fatalf("second read: %+v", got)
	}
	if launch.HasUnread(env.Spool) {
		t.Fatal("everything was read")
	}
}

func TestAnItemLongerThanTheCapIsStillDeliveredTruncated(t *testing.T) {
	r, st := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	if _, err := st.QueueCommand("instruct", "", strings.Repeat("z", 500), nil); err != nil {
		t.Fatal(err)
	}
	resp := call(t, env, "m1", "read_inbox", `{"max_chars":100}`)
	var out InboxRead
	if !resp.OK || json.Unmarshal(resp.Output, &out) != nil || len(out.Items) != 1 {
		t.Fatalf("%+v", resp)
	}
	if n := len(launch.InboxLine(out.Items[0])); n > 100 || !strings.Contains(out.Items[0].Summary, "…") {
		t.Fatalf("rendered %d chars: %q", n, out.Items[0].Summary)
	}
	if unread, _ := st.Inbox(true); len(unread) != 0 {
		t.Fatalf("the item was delivered: %+v", unread)
	}
}

func TestReadInboxWaitsUpToTheBoundAndReturnsEarlyOnAnItem(t *testing.T) {
	old := waitPoll
	waitPoll = 5 * time.Millisecond
	t.Cleanup(func() { waitPoll = old })
	r, st := newRuns(t)
	env := startRun(t, r, RoleArchwright)

	start := time.Now()
	resp := call(t, env, "w1", "read_inbox", `{"wait_seconds":1}`)
	var out InboxRead
	if !resp.OK || json.Unmarshal(resp.Output, &out) != nil || len(out.Items) != 0 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("an empty inbox must wait out the bound: %+v after %v", resp, time.Since(start))
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		st.QueueCommand("instruct", "", "wake", nil)
	}()
	start = time.Now()
	resp = call(t, env, "w2", "read_inbox", `{"wait_seconds":30}`)
	if !resp.OK || json.Unmarshal(resp.Output, &out) != nil || len(out.Items) != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("an item must end the wait: %+v after %v", resp, time.Since(start))
	}
}

func TestReadInboxRefusesAWaitOutsideItsBounds(t *testing.T) {
	r, _ := newRuns(t)
	env := startRun(t, r, RoleArchwright)
	for i, in := range []string{`{"wait_seconds":46}`, `{"wait_seconds":-1}`, `{"max_chars":-5}`} {
		if resp := call(t, env, "b"+string(rune('a'+i)), "read_inbox", in); resp.OK || resp.Error.Code != "invalid_input" {
			t.Fatalf("%s: %+v", in, resp)
		}
	}
}
