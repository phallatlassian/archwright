package works

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/phallatlassian/archwright/internal/forge"
	"github.com/phallatlassian/archwright/internal/remote"
	"github.com/phallatlassian/archwright/internal/store"
)

func init() {
	catalogue["record_pr"] = tool{roles: []string{RoleWarden}, call: recordPR}
}

// OpenPRStep is the Plan step whose Warden pushes the Job's branch and opens
// its pull request (ADR 0021, 0028). Review before it is Plan ordering only.
const OpenPRStep = "open-pr"

// PRRecorded is the answer to record_pr.
type PRRecorded struct {
	Status  string `json:"status"` // "recorded", or "exists" when the Job already had this PR
	URL     string `json:"url"`
	Number  int    `json:"number"`
	Message string `json:"message,omitempty"`
}

// recordPR is the one Forge tool left (ADR 0028). The Warden of the open-pr
// step pushes and opens the PR itself, with gh, and tells the Works which PR it
// is. The Works does not call the Forge to check: it checks the URL against the
// Site's remote, which is what stops a Warden recording a PR of some other
// repository, and the number against the URL, which catches a slip. A Job has
// one PR: a repeat of the recorded one answers "exists" and any other is a
// conflict.
func recordPR(c *callCtx, in json.RawMessage) (any, error) {
	var args struct {
		URL    string `json:"url"`
		Number int    `json:"number"`
	}
	if err := decode(in, &args); err != nil {
		return nil, err
	}
	if c.run.ctx.Job == "" {
		return nil, fmt.Errorf("%w: this run is not bound to a Job", errInvalidInput)
	}
	if c.run.ctx.Review != "" {
		return nil, fmt.Errorf("%w: a Review run is read-only and records nothing", errInvalidInput)
	}
	if err := requireField("url", args.URL); err != nil {
		return nil, err
	}
	ref, err := forge.ParsePRURL(args.URL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidInput, err)
	}
	if args.Number != ref.Number {
		return nil, fmt.Errorf("%w: number %d is not the pull request number in %s (%d)", errInvalidInput, args.Number, args.URL, ref.Number)
	}
	job, err := c.st.GetJob(c.run.ctx.Job)
	if err != nil {
		return nil, err
	}
	if job.Outcome != "" {
		return nil, fmt.Errorf("job %q is %s: no pull request is recorded for it: %w", job.ID, job.Outcome, store.ErrConflict)
	}
	switch job.PR {
	case args.URL:
		return PRRecorded{Status: "exists", URL: job.PR, Number: ref.Number, Message: "This Job already has this pull request recorded."}, nil
	case "":
	default:
		return nil, fmt.Errorf("job %q already has its pull request, %s: a Job has one: %w", job.ID, job.PR, store.ErrConflict)
	}
	if ok, err := c.carriesOutOpenPR(job); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("a pull request is recorded only during the %s step; it is not running for job %q: %w", OpenPRStep, job.ID, store.ErrConflict)
	}
	if job.Site == "" {
		return nil, fmt.Errorf("%w: this Job has no Site, so there is no remote to check the pull request against", errInvalidInput)
	}
	site, err := c.st.GetSite(job.Site)
	if err != nil {
		return nil, err
	}
	want, err := remote.Normalize(site.URL)
	if err != nil {
		return nil, fmt.Errorf("%w: Site %s has no usable remote: %v", errInvalidInput, site.Name, err)
	}
	if !sameRepo(want, ref.Repo) {
		return nil, fmt.Errorf("%w: %s is a pull request of %s, but Site %s is %s", errInvalidInput, args.URL, ref.Repo, site.Name, want)
	}
	if err := c.st.RecordPR(job.ID, args.URL); err != nil {
		return nil, err
	}
	c.runs.watchPR(job.ID)
	return PRRecorded{Status: "recorded", URL: args.URL, Number: ref.Number}, nil
}

// sameRepo compares two normalised remote identities, host and path alike
// without regard to case: GitHub and its like treat owner and repository names
// that way.
func sameRepo(a, b string) bool {
	return strings.EqualFold(a, b)
}

// carriesOutOpenPR says whether the calling Warden carries out the Job's
// running open-pr step.
func (c *callCtx) carriesOutOpenPR(job store.Job) (bool, error) {
	if c.run.ctx.Task != OpenPRStep {
		return false, nil
	}
	sr, err := c.st.GetStepRun(job.ID, OpenPRStep)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil && sr.Status == store.StepRunning, err
}
