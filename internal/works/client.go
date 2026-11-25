package works

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/phallatlassian/archwright/internal/envsnap"
	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/spool"
)

// Spawner starts a detached Works. The default re-executes this binary.
type Spawner func(d paths.Dirs) error

// ExecSpawner starts `<exe> works serve` detached in its own session, logging
// to the state directory, so the Works outlives the invoking command.
func ExecSpawner(d paths.Dirs) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := LogFile(d)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "works", "serve")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// slowTimeout bounds DoSlow: room for the doctor, which starts a throwaway tmux
// server and delivers a hook, to run before the Works answers.
const slowTimeout = 10 * time.Minute

// Client talks to a running Works.
type Client struct {
	Info Info
	http *http.Client
	// slow serves calls that legitimately outlast the usual 10s, such as
	// POST /runs waiting on the doctor preflight, which probes every runner.
	slow *http.Client
}

func newClient(info Info) *Client {
	return &Client{Info: info, http: &http.Client{Timeout: 10 * time.Second}, slow: &http.Client{Timeout: slowTimeout}}
}

// Connect returns a client for the running Works, starting one with spawn if
// none answers. A running instance is always reused.
func Connect(ctx context.Context, d paths.Dirs, spawn Spawner) (*Client, error) {
	if err := d.Ensure(); err != nil {
		return nil, err
	}
	if c := probe(d); c != nil {
		return c, nil
	}
	if err := spawn(d); err != nil {
		return nil, fmt.Errorf("starting works: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c := probe(d); c != nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("works did not start; see %s", d.Log())
}

// probe returns a client if the pid file points at a live, authorised Works.
func probe(d paths.Dirs) *Client {
	info, err := ReadInfo(d)
	if err != nil {
		return nil
	}
	c := newClient(info)
	var h Health
	if err := c.Do("GET", "/health", nil, &h); err != nil || h.PID != info.PID {
		return nil
	}
	return c
}

// Running reports whether a Works is up, without starting one.
func Running(d paths.Dirs) (*Client, bool) {
	c := probe(d)
	return c, c != nil
}

// Do performs one API call, decoding the JSON reply into out (if non-nil).
func (c *Client) Do(method, path string, in, out any) error {
	return c.do(c.http, method, path, in, out)
}

// DoSlow is Do for a call the Works may take minutes to answer.
func (c *Client) DoSlow(method, path string, in, out any) error {
	return c.do(c.slow, method, path, in, out)
}

func (c *Client) do(hc *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+c.Info.Addr()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Info.Token)
	if os.Getenv(spool.EnvRun) == "" { // a Session's own calls carry the Session's environment, not the developer's shell's
		req.Header.Set(EnvHeader, envsnap.Encode(envsnap.Capture(os.Environ())))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
