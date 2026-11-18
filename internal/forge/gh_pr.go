package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/phallatlassian/archwright/internal/remote"
)

// run executes gh with args and optional stdin, bounded by the adapter's
// timeout, and classifies a failure.
func (g GH) run(ctx context.Context, stdin string, args ...string) (string, error) {
	bin := g.Bin
	if bin == "" {
		bin = "gh"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", &Error{Kind: KindOther, Detail: "gh is not installed or not on PATH"}
	}
	timeout := g.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", &Error{Kind: KindNetwork, Detail: "gh timed out"}
		}
		return "", classify(stderr.String(), err)
	}
	return stdout.String(), nil
}

// prFields is what ReadPR and the existing-PR lookup ask gh for.
const prFields = "url,number,title,body,state,isDraft,headRefName,baseRefName,mergeable,statusCheckRollup"

type ghPR struct {
	URL, Title, Body, State, Mergeable string
	Number                             int
	IsDraft                            bool
	HeadRefName, BaseRefName           string
	StatusCheckRollup                  []struct {
		Typename   string `json:"__typename"`
		Name       string // CheckRun
		Status     string
		Conclusion string
		DetailsURL string `json:"detailsUrl"`
		Context    string // StatusContext
		State      string
		TargetURL  string `json:"targetUrl"`
	}
}

func (p ghPR) pr() PullRequest {
	out := PullRequest{URL: p.URL, Number: p.Number, Title: p.Title, Body: p.Body, State: p.State, Draft: p.IsDraft,
		Head: p.HeadRefName, Base: p.BaseRefName, Mergeable: p.Mergeable}
	for _, c := range p.StatusCheckRollup {
		if c.Typename == "StatusContext" || (c.Name == "" && c.Context != "") {
			ck := Check{Name: c.Context, URL: c.TargetURL}
			switch strings.ToUpper(c.State) { // commit statuses: PENDING, SUCCESS, FAILURE, ERROR
			case "PENDING", "EXPECTED":
				ck.Status = "in_progress"
			default:
				ck.Status, ck.Conclusion = "completed", strings.ToLower(c.State)
			}
			out.Checks = append(out.Checks, ck)
			continue
		}
		out.Checks = append(out.Checks, Check{Name: c.Name, Status: strings.ToLower(c.Status), Conclusion: strings.ToLower(c.Conclusion), URL: c.DetailsURL})
	}
	return out
}

// repoOf names the repository of a Site's remote URL as gh's HOST/OWNER/REPO.
func repoOf(siteURL string) (string, error) {
	key, err := remote.Normalize(siteURL)
	if err != nil {
		return "", &Error{Kind: KindOther, Detail: "the Site's remote URL names no repository: " + err.Error()}
	}
	return key, nil
}

// OpenPR looks for an open pull request from req.Head into req.Base and
// returns it; only if there is none does it run `gh pr create`.
func (g GH) OpenPR(ctx context.Context, req OpenPRRequest) (PullRequest, error) {
	repo, err := repoOf(req.SiteURL)
	if err != nil {
		return PullRequest{}, err
	}
	switch {
	case req.Head == "" || req.Base == "" || req.Head == req.Base:
		return PullRequest{}, &Error{Kind: KindOther, Detail: "a pull request needs distinct head and base branches"}
	case strings.HasPrefix(req.Head, "-") || strings.HasPrefix(req.Base, "-"):
		return PullRequest{}, &Error{Kind: KindOther, Detail: "head and base must be plain branch names"}
	}
	out, err := g.run(ctx, "", "pr", "list", "-R", repo, "--head", req.Head, "--base", req.Base, "--state", "open", "--limit", "1", "--json", prFields)
	if err != nil {
		return PullRequest{}, err
	}
	var found []ghPR
	if err := json.Unmarshal([]byte(out), &found); err != nil {
		return PullRequest{}, &Error{Kind: KindOther, Detail: "gh returned unreadable output: " + err.Error()}
	}
	if len(found) > 0 {
		pr := found[0].pr()
		pr.Existing = true
		return pr, nil
	}
	out, err = g.run(ctx, req.Body, "pr", "create", "-R", repo, "--head", req.Head, "--base", req.Base, "--title="+req.Title, "--body-file", "-")
	if err != nil {
		return PullRequest{}, err
	}
	u := prURLIn(out)
	if u == "" {
		return PullRequest{}, &Error{Kind: KindOther, Detail: "gh pr create printed no pull request URL"}
	}
	pr := PullRequest{URL: u, Title: req.Title, Body: req.Body, State: "OPEN", Head: req.Head, Base: req.Base}
	pr.Number = prNumber(u)
	return pr, nil
}

var prURLRe = regexp.MustCompile(`https?://\S+/pull/\d+`)

func prURLIn(s string) string { return prURLRe.FindString(s) }

func prNumber(u string) int {
	n, _ := strconv.Atoi(u[strings.LastIndex(u, "/")+1:])
	return n
}

// ReadPR runs `gh pr view URL --json ...`.
func (g GH) ReadPR(ctx context.Context, prURL string) (PullRequest, error) {
	if prURLIn(prURL) != prURL {
		return PullRequest{}, &Error{Kind: KindOther, Detail: "not a pull request URL"}
	}
	out, err := g.run(ctx, "", "pr", "view", prURL, "--json", prFields)
	if err != nil {
		return PullRequest{}, err
	}
	var p ghPR
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		return PullRequest{}, &Error{Kind: KindOther, Detail: "gh returned unreadable output: " + err.Error()}
	}
	pr := p.pr()
	if pr.URL == "" {
		pr.URL = prURL
	}
	return pr, nil
}

// CommentPR runs `gh pr comment URL --body-file -`, the body on stdin.
func (g GH) CommentPR(ctx context.Context, prURL, body string) error {
	if prURLIn(prURL) != prURL {
		return &Error{Kind: KindOther, Detail: "not a pull request URL"}
	}
	_, err := g.run(ctx, body, "pr", "comment", prURL, "--body-file", "-")
	return err
}
