package works

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// Starting a run waits on the doctor gate's probes, so POST /runs gets a far
// longer timeout than the other calls.
func TestStartingARunOutwaitsOrdinaryCalls(t *testing.T) {
	oldCall, oldStart := callTimeout, startTimeout
	callTimeout, startTimeout = 50*time.Millisecond, 5*time.Second
	t.Cleanup(func() { callTimeout, startTimeout = oldCall, oldStart })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(200)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	c := newClient(Info{Port: port})

	if err := c.Do("POST", "/runs", map[string]string{}, nil); err != nil {
		t.Errorf("POST /runs timed out: %v", err)
	}
	if err := c.Do("GET", "/health", nil, nil); err == nil {
		t.Error("an ordinary call should still time out")
	}
}
