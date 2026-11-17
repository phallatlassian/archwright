package works

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
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
