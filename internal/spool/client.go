package spool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// DefaultWait is how long a call waits for its response (ADR 0018).
const DefaultWait = 60 * time.Second

// ErrTimeout means no response arrived in time: the Works is unreachable.
var ErrTimeout = errors.New("works_unreachable: no response from the Works")

// Env identifies a run to the Works.
type Env struct {
	Run   string
	Dir   string
	Token string
}

// EnvFrom reads the run's environment variables.
func EnvFrom(getenv func(string) string) (Env, error) {
	e := Env{Run: getenv(EnvRun), Dir: getenv(EnvSpool), Token: getenv(EnvToken)}
	if e.Run == "" || e.Dir == "" || e.Token == "" {
		return e, fmt.Errorf("not inside a run: %s, %s and %s must be set", EnvRun, EnvSpool, EnvToken)
	}
	return e, nil
}

// Call sends one tool call and waits up to wait for the response. Re-sending
// an id that is already in the spool does not duplicate the request. The
// call's files are removed once the response is read, and an unanswered
// request is withdrawn on timeout.
func Call(ctx context.Context, env Env, id, tool string, input json.RawMessage, wait time.Duration) (Response, error) {
	if !ValidID(id) {
		return Response{}, fmt.Errorf("invalid request id %q", id)
	}
	if _, err := WriteRequest(env.Dir, Request{ID: id, Token: env.Token, Tool: tool, Input: input}); err != nil {
		return Response{}, fmt.Errorf("writing request: %w", err)
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		resp, err := ReadResponse(env.Dir, id)
		if err == nil {
			Remove(env.Dir, id)
			return resp, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Response{}, err
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-deadline.C:
			os.Remove(RequestPath(env.Dir, id))
			return Response{}, ErrTimeout
		case <-tick.C:
		}
	}
}
