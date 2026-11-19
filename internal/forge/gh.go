package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// defaultTimeout bounds one gh call; a hang is reported as a network fault.
const defaultTimeout = 30 * time.Second

// GH is the adapter that shells out to the developer's own gh, reusing its
// stored credentials so no secret enters Archwright's configuration (ADR 0022).
// Bin is the gh executable; empty means "gh" found on PATH at call time. Git
// is the git executable pushes run, likewise. Timeout bounds one call; zero
// means 30 seconds for gh and 2 minutes for a push.
type GH struct {
	Bin     string
	Git     string
	Timeout time.Duration
}

// ReadIssue runs `gh issue view URL --json ...`.
func (g GH) ReadIssue(ctx context.Context, issueURL string) (Issue, error) {
	stdout, err := g.run(ctx, "", "issue", "view", issueURL, "--json", "title,body,state,author,labels,url")
	if err != nil {
		return Issue{}, err
	}
	var out struct {
		URL, Title, Body, State string
		Author                  struct{ Login string }
		Labels                  []struct{ Name string }
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
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
	authMarkers = []string{"gh auth login", "not logged in", "authentication", "could not read username", "could not read password", "permission denied (publickey)", "http 401", "bad credentials", "gh_token"}
	netMarkers  = []string{"could not resolve host", "no such host", "dial tcp", "connection refused", "i/o timeout", "network is unreachable", "timeout", "tls", "x509", "unable to access", "could not read from remote repository"}
)

// Classify sorts the output of a failed gh call into a fault class, as the
// adapter does for its own calls.
func Classify(output string, err error) *Error { return classify(output, err) }

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

// pushTimeout bounds one git push.
const pushTimeout = 2 * time.Minute

// PushArgs builds the argv of the git push for req, or refuses it. It is the
// only place a push command is made, so what it never builds is impossible: no
// force flag, no "+" refspec, no other remote than origin, and never the
// Site's default branch. The refspec names the branch on both sides, so
// nothing is deleted or renamed either.
func PushArgs(req PushRequest) ([]string, error) {
	b := req.Branch
	switch {
	case req.ClonePath == "":
		return nil, errors.New("no clone to push from")
	case b == "":
		return nil, errors.New("no branch to push")
	case strings.HasPrefix(b, "-") || strings.HasPrefix(b, "+") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock"),
		strings.ContainsAny(b, ":*?[\\^~ \t\r\n\x00") || strings.Contains(b, "..") || strings.Contains(b, "@{") || strings.Contains(b, "//"),
		b == "HEAD", b == "@", strings.HasPrefix(b, "refs/"):
		return nil, fmt.Errorf("refusing to push %q: not a plain branch name", b)
	case req.DefaultBranch == "":
		return nil, errors.New("the Site's default branch is unknown, so the push cannot be proven safe")
	case b == req.DefaultBranch || b == strings.TrimPrefix(req.DefaultBranch, "refs/heads/"):
		return nil, fmt.Errorf("refusing to push the Site's default branch %q", b)
	}
	return []string{"-C", req.ClonePath, "push", "--porcelain", "--no-verify", "origin", "refs/heads/" + b + ":refs/heads/" + b}, nil
}

// PushBranch implements Forge with git, which reuses the developer's stored
// git credentials like gh does. Git is Git, or "git" on PATH.
func (g GH) PushBranch(ctx context.Context, req PushRequest) error {
	args, err := PushArgs(req)
	if err != nil {
		return &Error{Kind: KindOther, Detail: err.Error()}
	}
	bin := g.Git
	if bin == "" {
		bin = "git"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return &Error{Kind: KindOther, Detail: "git is not installed or not on PATH"}
	}
	timeout := g.Timeout
	if timeout == 0 {
		timeout = pushTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	for _, e := range os.Environ() { // the clone is named by -C, not by the Works' own environment
		if !strings.HasPrefix(e, "GIT_DIR=") && !strings.HasPrefix(e, "GIT_WORK_TREE=") && !strings.HasPrefix(e, "GIT_INDEX_FILE=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr // --porcelain reports a rejected ref on stdout
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &Error{Kind: KindNetwork, Detail: "git push timed out"}
		}
		return classify(stderr.String(), err)
	}
	return nil
}
