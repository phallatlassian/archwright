package works

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// ReviewGuild is the Guild whose Warden carries out reviews (ADR 0012).
const ReviewGuild = "hallmark"

// MaxReviewReworks is how many times a failed review sends the Job back to
// Masonry automatically; the next fail parks the Job (ADR 0012).
const MaxReviewReworks = 1

// ReviewRequest is what a ReviewRunner is given to carry out one review: a
// fresh Warden run that reports through the spool in Env. Nothing of
// Masonry's context is in it, only the code and the spec to judge it by.
type ReviewRequest struct {
	Env      RunEnv
	Job      string
	ReviewID string
	Worktree string // a detached, read-only checkout of the code under review
	Brief    string
}

// ReviewRunner carries out one review run (ADR 0003). An error, or returning
// without a report, errors the review: it says nothing about the work.
type ReviewRunner interface {
	RunReview(ctx context.Context, req ReviewRequest) error
}

// ConfigureReview sets the Runner reviews run on. Without one request_review fails closed.
func (r *Runs) ConfigureReview(rr ReviewRunner) {
	r.mu.Lock()
	r.reviewer = rr
	r.mu.Unlock()
}

var reviewTools = map[string]tool{
	"link_writ":           {roles: []string{RoleArchwright}, call: linkWrit},
	"request_review":      {roles: []string{RoleArchwright}, call: requestReview},
	"show_review":         {roles: []string{RoleArchwright}, call: showReview},
	"issue_review_rework": {roles: []string{RoleArchwright}, call: issueReviewRework},
	"pr_go_ahead":         {roles: []string{RoleArchwright}, call: prGoAhead},
	"report_review":       {roles: []string{RoleWarden}, call: reportReview},
	"pr_go_ahead_status":  {roles: []string{RoleWarden}, call: prGoAheadStatus},
}

func init() {
	for name, t := range reviewTools {
		catalogue[name] = t
	}
}

func linkWrit(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		JobID string `json:"job_id"`
		Writ  string `json:"writ"` // the ticket's text, or a pointer to it
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if err := requireField("job_id", a.JobID); err != nil {
		return nil, err
	}
	if blank(a.Writ) {
		return nil, fmt.Errorf("%w: writ is required", errInvalidInput)
	}
	return c.st.SetWrit(a.JobID, a.Writ)
}

// specMaterial is what a review's Spec axis judges the work against: the
// writ if one is linked, else the Plan steps' briefs and the Job's original
// request, else nothing (ADR 0012). The returned basis names which it used.
// A title alone is a label, not a request, so it is no basis.
func specMaterial(st *store.Store, job store.Job) (basis, material string, err error) {
	if !blank(job.Writ) {
		return store.SpecWrit, "Writ:\n\n" + strings.TrimSpace(job.Writ) + "\n", nil
	}
	var briefs []string
	if plan, err := st.GetPlan(job.ID); err == nil {
		for _, s := range plan.Steps {
			if s.Guild != ReviewGuild && !blank(s.Brief) {
				briefs = append(briefs, fmt.Sprintf("- step %q (%s), %s:\n%s", s.ID, s.Guild, s.Title, indent(strings.TrimSpace(s.Brief))))
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", "", err
	}
	if len(briefs) == 0 && blank(job.Description) {
		return store.SpecNone, "", nil
	}
	var w strings.Builder
	fmt.Fprintf(&w, "Original request: %s\n", job.Title)
	if !blank(job.Description) {
		w.WriteString("\n" + strings.TrimSpace(job.Description) + "\n")
	}
	if len(briefs) > 0 {
		w.WriteString("\nPlan step briefs:\n\n" + strings.Join(briefs, "\n") + "\n")
	}
	return store.SpecPlanAndRequest, w.String(), nil
}

func indent(s string) string { return "  " + strings.ReplaceAll(s, "\n", "\n  ") }

// renderReviewBrief is everything the review Warden is told.
func renderReviewBrief(g Guild, rev store.Review, worktree, specMaterial string) string {
	var w strings.Builder
	fmt.Fprintf(&w, "# Review %d\n\nYou are a fresh Warden. You have seen none of the work that produced this code, and you are told nothing of how it was built.\n", rev.Seq)
	fmt.Fprintf(&w, "\n## What to review\n\nThe code at commit %s (branch %s), checked out read-only and detached at:\n\n    %s\n\n"+
		"Read it, run read-only commands in it, report. Do not change, commit or push anything: Review reports only. Do not re-run the Site's gates; the Works already did.\n", rev.SHA, rev.Branch, worktree)
	w.WriteString("\n## Standards axis\n\nDoes the code follow this repository's documented conventions (its AGENTS.md, GLOSSARY.md, ADRs and the Guild standards below)? Judge only against what is written down.\n")
	w.WriteString("\n## Spec axis\n\nDoes the code do what was asked for?\n\n")
	switch rev.SpecBasis {
	case store.SpecWrit:
		w.WriteString("The spec is the writ linked to this Job:\n\n")
	case store.SpecPlanAndRequest:
		w.WriteString("No writ is linked, so the spec is the Plan step briefs and the Job's original request:\n\n")
	default:
		w.WriteString("There is no spec to judge by: no writ, no Plan step brief and no request. Report the Spec axis as not_evaluated and say so.\n")
	}
	w.WriteString(specMaterial)
	fmt.Fprintf(&w, "\n## %s Guild standards\n\n", g.Name)
	if s := strings.TrimSpace(g.Standards); s != "" {
		w.WriteString(s + "\n")
	} else {
		w.WriteString("(the Guild states no standards)\n")
	}
	w.WriteString("\n## Reporting\n\nCall `archwright tool report_review` with JSON on stdin, one verdict per axis, each `pass`, `fail` or `not_evaluated`:\n" +
		"`{\"standards\":{\"verdict\":\"pass|fail|not_evaluated\",\"reason\":\"...\",\"findings\":[{\"file\":\"path\",\"line\":12,\"severity\":\"blocker|major|minor\",\"reason\":\"...\"}]},\"spec\":{...}}`.\n" +
		"A `fail` needs at least one finding; a `pass` may carry only minor findings; `not_evaluated` needs a reason and no findings. Every finding names a file, a line, a severity and a reason. The axes are independent: one may pass while the other fails.\n")
	return w.String()
}

// ReviewInfo is the answer to request_review.
type ReviewInfo struct {
	ReviewID  string `json:"review_id"`
	Seq       int    `json:"seq"`
	Status    string `json:"status"`
	SHA       string `json:"sha"`
	SpecBasis string `json:"spec_basis"`
}

func requestReview(c *callCtx, in json.RawMessage) (any, error) {
	id, err := jobArg(in)
	if err != nil {
		return nil, err
	}
	rev, err := c.runs.startReview(id)
	if err != nil {
		return nil, err
	}
	return ReviewInfo{ReviewID: rev.ID, Seq: rev.Seq, Status: rev.Status, SHA: rev.SHA, SpecBasis: rev.SpecBasis}, nil
}

// startReview issues Review for a Job as a separate task after Masonry's
// handback: a fresh Warden run in a detached read-only worktree (ADR 0012).
func (r *Runs) startReview(jobID string) (store.Review, error) {
	job, err := r.st.GetJob(jobID)
	if err != nil {
		return store.Review{}, err
	}
	if job.ParkedFor != "" {
		return store.Review{}, fmt.Errorf("job %q is parked for the Archwright: %w", job.ID, store.ErrConflict)
	}
	if job.Site == "" {
		return store.Review{}, fmt.Errorf("%w: the Job has no Site to review", errInvalidInput)
	}
	r.mu.Lock()
	reviewer, sm, src, root, closed := r.reviewer, r.sites, r.guilds, r.spoolRoot, r.bg.Err() != nil
	r.mu.Unlock()
	switch {
	case closed:
		return store.Review{}, fmt.Errorf("%w: the Works is shutting down", errInvalidInput)
	case reviewer == nil:
		return store.Review{}, fmt.Errorf("%w: review runners arrive with the runner adapter, which is not yet available", errInvalidInput)
	case sm == nil:
		return store.Review{}, fmt.Errorf("%w: no Site manager is configured to check out the code", errInvalidInput)
	case src == nil:
		return store.Review{}, fmt.Errorf("%w: no guilds are configured", errInvalidInput)
	}
	g, err := src(ReviewGuild)
	if err != nil {
		return store.Review{}, err
	}

	hb, err := r.st.LatestHandback(jobID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Review{}, fmt.Errorf("nothing to review: Masonry has not handed back: %w", store.ErrConflict)
	}
	if err != nil {
		return store.Review{}, err
	}
	if hb.Status != HandbackDone {
		return store.Review{}, fmt.Errorf("the latest handback is %s; Review follows a done handback: %w", hb.Status, store.ErrConflict)
	}
	if gr, err := r.st.GateRunFor(hb.ID); err == nil && gr.Status == store.GateRunFailed {
		return store.Review{}, fmt.Errorf("the handback's gates failed; Masonry fixes those before Review: %w", store.ErrConflict)
	}
	reviews, err := r.st.Reviews(jobID)
	if err != nil {
		return store.Review{}, err
	}
	if n := len(reviews); n > 0 {
		last := reviews[n-1]
		if last.Status == store.ReviewRunning {
			return store.Review{}, fmt.Errorf("job %q already has a review running: %w", jobID, store.ErrConflict)
		}
		if last.ReworkedAt != nil && hb.ID <= last.ReworkFloor() {
			return store.Review{}, fmt.Errorf("the rework is issued but Masonry has not handed back since: %w", store.ErrConflict)
		}
	}
	if _, _, err := sm.IntegrationTip(jobID); err != nil {
		return store.Review{}, err
	}
	basis, material, err := specMaterial(r.st, job)
	if err != nil {
		return store.Review{}, err
	}
	wt, err := sm.CreateWorktree(jobID, store.KindReadOnly, "")
	if err != nil {
		return store.Review{}, err
	}
	rev, err := r.st.NewReview(jobID, hb.ID, wt.BaseRef, wt.BaseSHA, basis, func(v store.Review) string {
		return renderReviewBrief(g, v, wt.Path, material)
	})
	if err != nil {
		return store.Review{}, err
	}
	brief := rev.Brief
	env, err := r.StartFor(RoleWarden, root, RunContext{Job: jobID, Guild: g.Name, Review: rev.ID})
	if err != nil {
		r.errorReview(rev.ID, "starting the run: "+err.Error())
		return store.Review{}, err
	}
	if err := r.st.AttachReviewRun(rev.ID, env.Run); err != nil {
		r.End(env.Run)
		r.errorReview(rev.ID, "starting the run: "+err.Error())
		return store.Review{}, err
	}
	r.mu.Lock()
	if closed = r.bg.Err() != nil; !closed {
		r.wg.Add(1) // under mu, so Close cannot Wait between the check and the Add
	}
	r.mu.Unlock()
	if closed {
		r.End(env.Run)
		r.errorReview(rev.ID, "the Works is shutting down")
		return store.Review{}, fmt.Errorf("%w: the Works is shutting down", errInvalidInput)
	}
	go func() {
		defer r.wg.Done()
		runErr := reviewer.RunReview(r.bg, ReviewRequest{Env: env, Job: jobID, ReviewID: rev.ID, Worktree: wt.Path, Brief: brief})
		r.End(env.Run)
		msg := "the run ended without reporting a verdict"
		if runErr != nil {
			msg = "the run failed: " + runErr.Error()
		}
		r.errorReview(rev.ID, msg) // a no-op once it reported
	}()
	return rev, nil
}

// errorReview ends a review that never got a verdict; a failure to record it is logged.
func (r *Runs) errorReview(id, reason string) {
	if err := r.st.ErrorReview(id, reason); err != nil {
		log.Printf("review %s: recording that it errored: %v", id, err)
	}
}

// validateAxis checks an axis as a Warden reports it.
func validateAxis(name string, a store.Axis) error {
	for i, f := range a.Findings {
		switch {
		case blank(f.File) || f.Line < 1 || blank(f.Reason):
			return fmt.Errorf("%w: %s finding %d needs a file, a line from 1 and a reason", errInvalidInput, name, i+1)
		case f.Severity != store.SeverityBlocker && f.Severity != store.SeverityMajor && f.Severity != store.SeverityMinor:
			return fmt.Errorf("%w: %s finding %d: severity must be blocker, major or minor", errInvalidInput, name, i+1)
		}
	}
	switch a.Verdict {
	case store.VerdictFail:
		if len(a.Findings) == 0 {
			return fmt.Errorf("%w: a %s fail needs at least one finding", errInvalidInput, name)
		}
	case store.VerdictPass:
		for _, f := range a.Findings {
			if f.Severity != store.SeverityMinor {
				return fmt.Errorf("%w: a %s pass cannot carry a %s finding", errInvalidInput, name, f.Severity)
			}
		}
	case store.VerdictNotEvaluated:
		if blank(a.Reason) || len(a.Findings) > 0 {
			return fmt.Errorf("%w: %s not_evaluated needs a reason and no findings", errInvalidInput, name)
		}
	default:
		return fmt.Errorf("%w: %s verdict must be pass, fail or not_evaluated", errInvalidInput, name)
	}
	return nil
}

// overallOf combines the axes: any fail fails; only two passes pass; an axis
// that was not evaluated otherwise means the work cannot pass (ADR 0012).
func overallOf(standards, spec store.Axis) string {
	switch {
	case standards.Verdict == store.VerdictFail || spec.Verdict == store.VerdictFail:
		return store.VerdictFail
	case standards.Verdict == store.VerdictPass && spec.Verdict == store.VerdictPass:
		return store.VerdictPass
	}
	return store.VerdictNotEvaluated
}

func reportReview(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Standards store.Axis `json:"standards"`
		Spec      store.Axis `json:"spec"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if c.run.ctx.Review == "" {
		return nil, fmt.Errorf("%w: this run carries out no review", errInvalidInput)
	}
	if err := validateAxis("standards", a.Standards); err != nil {
		return nil, err
	}
	if err := validateAxis("spec", a.Spec); err != nil {
		return nil, err
	}
	rev, err := c.st.GetReview(c.run.ctx.Review)
	if err != nil {
		return nil, err
	}
	if rev.SpecBasis == store.SpecNone { // nothing to judge by: whatever was reported, the axis was not evaluated
		a.Spec = store.Axis{Verdict: store.VerdictNotEvaluated, Reason: "no writ, Plan step brief or request to judge the work against", Findings: []store.Finding{}}
	}
	if a.Standards.Findings == nil {
		a.Standards.Findings = []store.Finding{}
	}
	if a.Spec.Findings == nil {
		a.Spec.Findings = []store.Finding{}
	}
	overall := overallOf(a.Standards, a.Spec)
	rev, err = c.st.FinishReview(rev.ID, a.Standards, a.Spec, overall)
	if err != nil {
		return nil, err
	}
	next, err := c.runs.afterReview(rev)
	if err != nil {
		return nil, err
	}
	return map[string]any{"review_id": rev.ID, "overall": overall, "spec_basis": rev.SpecBasis, "next": next}, nil
}

// afterReview applies what a verdict means for the Job. A fail after the one
// automatic rework parks the Job in the Needs-you inbox; a first fail waits
// for the Archwright to issue that rework; a verdict that cannot pass for
// want of evaluation asks the developer.
func (r *Runs) afterReview(rev store.Review) (string, error) {
	job, err := r.st.GetJob(rev.JobID)
	if err != nil {
		return "", err
	}
	switch rev.Overall {
	case store.VerdictPass:
		return "pr_go_ahead", nil
	case store.VerdictFail:
		used, err := r.st.ReworkRounds(rev.JobID)
		if err != nil {
			return "", err
		}
		if used < MaxReviewReworks {
			return "issue_review_rework", nil
		}
		reason := fmt.Sprintf("review %d failed again after the automatic rework: the developer decides", rev.Seq)
		err = r.st.ParkJob(rev.JobID, reason, store.NewNeedsYou{
			Kind: store.NeedsReviewParked, JobID: rev.JobID, Site: job.Site, DedupeKey: store.NeedsReviewParked + ":" + rev.JobID,
			Summary: fmt.Sprintf("Job %q is parked: it failed review twice", job.Title),
			Detail:  map[string]any{"review": rev.ID, "standards": rev.Standards, "spec": rev.Spec},
		})
		return "parked", err
	default:
		_, err := r.st.AddNeedsYou(store.NewNeedsYou{
			Kind: store.NeedsReviewNotEvaluated, JobID: rev.JobID, Site: job.Site, DedupeKey: store.NeedsReviewNotEvaluated + ":" + rev.ID,
			Summary: fmt.Sprintf("Review of job %q could not evaluate every axis, so it cannot pass", job.Title),
			Detail:  map[string]any{"review": rev.ID, "standards": rev.Standards, "spec": rev.Spec},
		})
		return "needs_you", err
	}
}

// reworkConstraints turns a failed review's findings into constraints for Masonry.
func reworkConstraints(rev store.Review) []string {
	var out []string
	for _, ax := range []struct {
		name string
		a    *store.Axis
	}{{"standards", rev.Standards}, {"spec", rev.Spec}} {
		if ax.a == nil || ax.a.Verdict != store.VerdictFail {
			continue
		}
		for _, f := range ax.a.Findings {
			out = append(out, fmt.Sprintf("Review %s finding (%s) at %s:%d: %s", ax.name, f.Severity, f.File, f.Line, f.Reason))
		}
	}
	return out
}

// ReviewState is what the Archwright sees of a Job's reviews.
type ReviewState struct {
	Reviews []store.Review `json:"reviews"`
	Next    string         `json:"next"`
}

// reviewNext says what the Job's review state calls for.
func (r *Runs) reviewNext(job store.Job, reviews []store.Review) string {
	if job.ParkedFor != "" {
		return "parked"
	}
	if len(reviews) == 0 {
		return "request_review"
	}
	last := reviews[len(reviews)-1]
	switch {
	case last.Status == store.ReviewRunning:
		return "wait"
	case last.Status == store.ReviewErrored:
		return "request_review"
	case last.Overall == store.VerdictPass:
		if r.reviewStale(last) != nil {
			return "request_review"
		}
		if job.PRGoAhead != "" {
			return "go_ahead_granted"
		}
		return "pr_go_ahead"
	case last.Overall == store.VerdictFail:
		if last.ReworkedAt == nil {
			return "issue_review_rework"
		}
		if hb, err := r.st.LatestHandback(job.ID); err == nil && hb.ID > last.ReworkFloor() {
			return "request_review"
		}
		return "await_rework_handback"
	}
	return "needs_you"
}

// reviewStale reports why a review no longer speaks for the Job's code: a
// later handback, or an integration branch that has moved since.
func (r *Runs) reviewStale(rev store.Review) error {
	if hb, err := r.st.LatestHandback(rev.JobID); err != nil || hb.ID != rev.HandbackID {
		return fmt.Errorf("a handback has landed since this review: %w", store.ErrConflict)
	}
	r.mu.Lock()
	sm := r.sites
	r.mu.Unlock()
	if sm == nil {
		return fmt.Errorf("%w: no Site manager is configured to check the reviewed commit", errInvalidInput)
	}
	if _, sha, err := sm.IntegrationTip(rev.JobID); err != nil {
		return err
	} else if sha != rev.SHA {
		return fmt.Errorf("the integration branch has moved since this review of %s: %w", rev.SHA, store.ErrConflict)
	}
	return nil
}

func showReview(c *callCtx, in json.RawMessage) (any, error) {
	id, err := jobArg(in)
	if err != nil {
		return nil, err
	}
	job, err := c.st.GetJob(id)
	if err != nil {
		return nil, err
	}
	reviews, err := c.st.Reviews(id)
	if err != nil {
		return nil, err
	}
	// A second fail whose parking did not complete is finished here, so the Job is never left wedged.
	if n := len(reviews); n > 0 && job.ParkedFor == "" && reviews[n-1].Overall == store.VerdictFail && reviews[n-1].ReworkedAt == nil {
		if used, err := c.st.ReworkRounds(id); err == nil && used >= MaxReviewReworks {
			if _, err := c.runs.afterReview(reviews[n-1]); err != nil {
				return nil, err
			}
			job, _ = c.st.GetJob(id)
		}
	}
	return ReviewState{Reviews: reviews, Next: c.runs.reviewNext(job, reviews)}, nil
}

// ReworkInfo is the answer to issue_review_rework.
type ReworkInfo struct {
	ReviewID    string   `json:"review_id"`
	Constraints []string `json:"constraints"` // the findings, as constraints for Masonry's briefs
	Next        string   `json:"next"`
}

func issueReviewRework(c *callCtx, in json.RawMessage) (any, error) {
	id, err := jobArg(in)
	if err != nil {
		return nil, err
	}
	job, err := c.st.GetJob(id)
	if err != nil {
		return nil, err
	}
	if job.ParkedFor != "" {
		return nil, fmt.Errorf("job %q is parked for the Archwright: %w", id, store.ErrConflict)
	}
	last, err := c.st.LatestReview(id)
	if err != nil {
		return nil, fmt.Errorf("job %q has no review to rework after: %w", id, store.ErrConflict)
	}
	var floor int64
	if hb, err := c.st.LatestHandback(id); err == nil {
		floor = hb.ID
	}
	last, err = c.st.IssueRework(id, reworkConstraints(last), floor)
	if err != nil {
		return nil, err
	}
	return ReworkInfo{ReviewID: last.ID, Constraints: last.ReworkConstraints, Next: "Dispatch Masonry with these constraints in its briefs; when it hands back, request_review again."}, nil
}

func prGoAhead(c *callCtx, in json.RawMessage) (any, error) {
	id, err := jobArg(in)
	if err != nil {
		return nil, err
	}
	if _, err := c.st.GetJob(id); err != nil {
		return nil, err
	}
	last, err := c.st.LatestReview(id)
	if err != nil {
		return nil, fmt.Errorf("job %q has not been reviewed, and the PR go-ahead needs an overall pass: %w", id, store.ErrConflict)
	}
	if last.Status == store.ReviewReported && last.Overall == store.VerdictPass {
		if err := c.runs.reviewStale(last); err != nil { // a pass is for the code it saw
			return nil, err
		}
	}
	if err := c.st.GrantGoAhead(id, last.ID); err != nil {
		return nil, err
	}
	return map[string]any{"job_id": id, "review_id": last.ID, "granted": true}, nil
}

func prGoAheadStatus(c *callCtx, in json.RawMessage) (any, error) {
	if err := decode(in, &struct{}{}); err != nil {
		return nil, err
	}
	if _, err := c.warden(); err != nil {
		return nil, err
	}
	job, err := c.st.GetJob(c.run.ctx.Job)
	if err != nil {
		return nil, err
	}
	granted := false
	if last, err := c.st.LatestReview(job.ID); job.PRGoAhead != "" && err == nil && last.Status == store.ReviewReported && last.Overall == store.VerdictPass {
		granted = c.runs.reviewStale(last) == nil
	}
	return map[string]any{"granted": granted}, nil
}
