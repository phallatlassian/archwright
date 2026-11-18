package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// defaultTimeout bounds one gh call; a hang is reported as a network fault.
const defaultTimeout = 30 * time.Second

// GH is the adapter that shells out to the developer's own gh, reusing its
// stored credentials so no secret enters Archwright's configuration (ADR 0022).
// Bin is the gh executable; empty means "gh" found on PATH at call time.
// Timeout bounds one call; zero means 30 seconds.
type GH struct {
	Bin     string
	Timeout time.Duration
}

// ReadIssue runs `gh issue view URL --json ...`.
func (g GH) ReadIssue(ctx context.Context, issueURL string) (Issue, error) {
	bin := g.Bin
	if bin == "" {
		bin = "gh"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return Issue{}, &Error{Kind: KindOther, Detail: "gh is not installed or not on PATH"}
	}
	timeout := g.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "issue", "view", issueURL, "--json", "title,body,state,author,labels,url")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Issue{}, &Error{Kind: KindNetwork, Detail: "gh timed out"}
		}
		return Issue{}, classify(stderr.String(), err)
	}
	var out struct {
		URL, Title, Body, State string
		Author                  struct{ Login string }
		Labels                  []struct{ Name string }
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return Issue{}, &Error{Kind: KindOther, Detail: "gh returned unreadable output: " + err.Error()}
	}
	is := Issue{URL: out.URL, Title: out.Title, Body: out.Body, State: out.State, Author: out.Author.Login}
	if is.URL == "" {
		is.URL = issueURL
	}
	for _, l := range out.Labels {
		is.Labels = append(is.Labels, l.Name)
	}
	return is, nil
}

var (
	authMarkers = []string{"gh auth login", "not logged in", "authentication", "http 401", "bad credentials", "gh_token"}
	netMarkers  = []string{"could not resolve host", "no such host", "dial tcp", "connection refused", "i/o timeout", "network is unreachable", "timeout", "tls", "x509"}
)

// classify sorts a failed gh call by its stderr.
func classify(stderr string, err error) *Error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = err.Error()
	}
	low := strings.ToLower(detail)
	for _, m := range authMarkers {
		if strings.Contains(low, m) {
			return &Error{Kind: KindAuth, Detail: detail}
		}
	}
	for _, m := range netMarkers {
		if strings.Contains(low, m) {
			return &Error{Kind: KindNetwork, Detail: detail}
		}
	}
	return &Error{Kind: KindOther, Detail: detail}
}
