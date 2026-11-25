package works

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/envsnap"
)

// POST /runs waits on the doctor preflight, which outlasts the usual client
// timeout: DoSlow must wait it out where Do gives up.
func TestDoSlowOutlastsTheDefaultTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(201)
		w.Write([]byte(`{"Run":"r1"}`))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	c := newClient(Info{Port: n})
	c.http.Timeout = 100 * time.Millisecond

	var env RunEnv
	if err := c.Do("POST", "/runs", nil, &env); err == nil {
		t.Fatal("Do should time out on a slow Works")
	}
	if err := c.DoSlow("POST", "/runs", nil, &env); err != nil || env.Run != "r1" {
		t.Fatalf("DoSlow = %v, %+v; want it to wait for the reply", err, env)
	}
}

func TestTheCLIReportsItsShellEnvironmentExceptFromInsideASession(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(EnvHeader)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	c := newClient(Info{Port: port, Token: "t"})

	t.Setenv("ARCHWRIGHT_RUN", "")
	t.Setenv("PATH", "/the/developers/path")
	t.Setenv("WORKS_TEST_SECRET", "nope")
	if err := c.Do("GET", "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	snap, err := envsnap.Decode(got)
	if err != nil || snap["PATH"] != "/the/developers/path" || snap["WORKS_TEST_SECRET"] != "" {
		t.Fatalf("header %q decodes to %v, %v", got, snap, err)
	}

	t.Setenv("ARCHWRIGHT_RUN", "run-1") // a call made by an agent in a Session: its environment is the Session's
	if err := c.Do("GET", "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("a Session's own call reported an environment: %q", got)
	}
}
