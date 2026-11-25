package works

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phallatlassian/archwright/internal/remote"
	"github.com/phallatlassian/archwright/internal/store"
)

// WardenRequest is what a WardenRunner is given to carry out one Plan step: a
// fresh Warden run that works through the spool in Env and ends by calling
// handback.
type WardenRequest struct {
	Env     RunEnv
	Job     string
	Guild   string
	Step    string
	Brief   string // the task brief stored with the step, plus a resume note after a restart
	Resumed bool   // the Works restarted while the step was running

	Worktree string            // a read-only checkout of the Job's code to run in; empty when the Job has no Site
	ExtraEnv map[string]string // further variables for the runner process, such as GIT_CONFIG_GLOBAL for delivered skills
	Trace    Trace             // where a runner reports its timeline and usage for the dashboard
}

// WardenRunner carries out one task Warden run (ADR 0020). An error, or
// returning without a handback, fails the step.
type WardenRunner interface {
	RunWarden(ctx context.Context, req WardenRequest) error
}

// ConfigureWarden sets the Runner Plan-step Wardens run on. Without one
// start_step fails closed.
func (r *Runs) ConfigureWarden(w WardenRunner) {
	r.mu.Lock()
	r.warden = w
	r.mu.Unlock()
}

func init() {
	catalogue["start_step"] = tool{roles: []string{RoleArchwright}, call: startStepTool}
}

// StepInfo is the answer to start_step.
type StepInfo struct {
	Job    string `json:"job"`
	Step   string `json:"step"`
	Guild  string `json:"guild"`
	Status string `json:"status"`
}

func startStepTool(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Job  string `json:"job"`
		Step string `json:"step"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if err := requireField("job", a.Job); err != nil {
		return nil, err
	}
	if err := requireField("step", a.Step); err != nil {
		return nil, err
	}
	sr, err := c.runs.startStep(a.Job, a.Step)
	if err != nil {
		return nil, err
	}
	return StepInfo{Job: sr.JobID, Step: sr.Step, Guild: sr.Guild, Status: sr.Status}, nil
}

// jobStartable refuses a step for a Job the developer paused, that is closed
// or that is parked for the Archwright.
func jobStartable(job store.Job) error {
	switch {
	case job.Outcome != "":
		return fmt.Errorf("job %q is %s: no more work is started for it: %w", job.ID, job.Outcome, store.ErrConflict)
	case job.PausedAt != "":
		return fmt.Errorf("job %q is paused by the developer: no step starts until it is resumed: %w", job.ID, store.ErrConflict)
	case job.ParkedFor != "":
		return fmt.Errorf("job %q is parked for the Archwright (%s): decide what to do before starting a step: %w", job.ID, job.ParkedFor, store.ErrConflict)
	}
	return nil
}

// startStep starts a Plan step by summoning a Warden for its Guild (ADR 0020).
func (r *Runs) startStep(jobID, stepID string) (store.StepRun, error) {
	job, err := r.st.GetJob(jobID)
	if err != nil {
		return store.StepRun{}, err
	}
	if err := jobStartable(job); err != nil {
		return store.StepRun{}, err
	}
	r.mu.Lock()
	warden, src, closed := r.warden, r.guilds, r.bg.Err() != nil
	reg := r.defs
	r.mu.Unlock()
	switch {
	case closed:
		return store.StepRun{}, fmt.Errorf("%w: the Works is shutting down", errInvalidInput)
	case warden == nil:
		return store.StepRun{}, fmt.Errorf("%w: no Warden runner is configured", errInvalidInput)
	case src == nil:
		return store.StepRun{}, fmt.Errorf("%w: no guilds are configured", errInvalidInput)
	}
	prior, err := r.priorWork(job)
	if err != nil {
		return store.StepRun{}, err
	}
	sr, err := r.st.StartStep(jobID, stepID, func(plan store.Plan, runs []store.StepRun) (string, string, error) {
		if !plan.Cleared() {
			return "", "", fmt.Errorf("the Plan of job %q is %s: the developer must approve an ad hoc Plan before a step starts: %w", jobID, plan.Approval, store.ErrConflict)
		}
		var step *store.Step
		for i := range plan.Steps {
			if plan.Steps[i].ID == stepID {
				step = &plan.Steps[i]
			}
		}
		if step == nil {
			return "", "", fmt.Errorf("job %q has no step %q: %w", jobID, stepID, store.ErrNotFound)
		}
		done := map[string]bool{}
		for _, sr := range runs {
			done[sr.Step] = sr.Status == store.StepHandedBack
		}
		var waiting []string
		for _, b := range step.BlockedBy {
			if !done[b] {
				waiting = append(waiting, b)
			}
		}
		if len(waiting) > 0 {
			return "", "", fmt.Errorf("step %q waits for %s to hand back: %w", stepID, strings.Join(waiting, ", "), store.ErrConflict)
		}
		g, err := src(step.Guild)
		if err != nil {
			return "", "", err
		}
		charter := ""
		if reg != nil {
			if dg, ok := reg.Guild(g.Name); ok {
				charter = dg.Charter
			}
		}
		return g.Name, renderStepBrief(job, *step, g, charter, prior), nil
	})
	if err != nil {
		return store.StepRun{}, err
	}
	if err := r.summon(sr, false); err != nil {
		return store.StepRun{}, err
	}
	return sr, nil
}

// wardenMD tells a Warden how to call its tools. They reach agents through a
// CLI, not injected MCP (ADR 0014), so a Warden that looks for MCP tools finds
// none and ends without handing back. It ships in the binary like the
// Archwright's instructions.
//
//go:embed warden.md
var wardenMD string

// renderStepBrief is the task brief a step's Warden starts from: the step, the
// Job's request and the Guild's charter and standards.
//
// prior is the work earlier steps left (see priorWork); a step's Inputs line
// names artifacts such as "fix.diff", which only resolve through it.
func renderStepBrief(job store.Job, step store.Step, g Guild, charter, prior string) string {
	var w strings.Builder
	fmt.Fprintf(&w, "# Task: %s\n\nYou are the %s Warden for step %q of job %s. Work through the Archwright tools: dispatch_wright, inspect_wright, rework_wright, accept_piece and finally handback, plus record_pr in the open-pr step; reach the Forge yourself with gh and git, as described below.\n", step.Title, g.Name, step.ID, job.ID)
	w.WriteString("\n" + strings.TrimSpace(wardenMD) + "\n")
	w.WriteString("\n## The Job's request\n\n" + strings.TrimSpace(job.Title))
	if d := strings.TrimSpace(job.Description); d != "" {
		w.WriteString("\n\n" + d)
	}
	if u := strings.TrimSpace(job.SourceIssue); u != "" {
		w.WriteString("\n\nSource issue: " + u + " (read it with `gh issue view`; put its URL in the inputs of a Wright's brief if the Wright needs it)")
	}
	w.WriteString("\n\n## This step\n\n" + step.Title)
	if b := strings.TrimSpace(step.Brief); b != "" {
		w.WriteString("\n\n" + b)
	}
	if p := strings.TrimSpace(prior); p != "" {
		w.WriteString("\n\n## Work so far\n\n" + p)
	}
	if c := strings.TrimSpace(charter); c != "" {
		fmt.Fprintf(&w, "\n\n## %s Guild charter\n\n%s", g.Name, c)
	}
	if s := strings.TrimSpace(g.Standards); s != "" {
		fmt.Fprintf(&w, "\n\n## %s Guild standards\n\n%s", g.Name, s)
	}
	if s := skillsSection(g.Skills, "`checked`"); s != "" {
		w.WriteString("\n\n" + strings.TrimSuffix(s, "\n"))
	}
	w.WriteString("\n")
	return w.String()
}

// priorWork tells a step's Warden where the Job's earlier work is: the
// integration branch every accepted piece was merged into, and those pieces
// by name. A step's Inputs, such as "fix.diff", are only labels; without this
// a reviewing Warden has nothing to inspect (a Warden's own checkout is empty
// by design). Empty when nothing was accepted yet.
func (r *Runs) priorWork(job store.Job) (string, error) {
	pieces, err := r.st.Pieces(job.ID)
	if err != nil {
		return "", err
	}
	var accepted []string
	for _, p := range pieces {
		if p.Status == store.PieceAccepted && !isMergeWright(p.Piece) {
			accepted = append(accepted, fmt.Sprintf("- %s (%s Guild)", p.Piece, p.Guild))
		}
	}
	if len(accepted) == 0 {
		return "", nil
	}
	var w strings.Builder
	r.mu.Lock()
	sm := r.sites
	r.mu.Unlock()
	if sm != nil && job.Site != "" {
		if t, err := sm.IntegrationPushTarget(job.ID); err == nil {
			fmt.Fprintf(&w, "The accepted pieces are merged into the Job's integration branch `%s` on Site %s, based on `%s`; the Inputs named in this step refer to that work. Your own checkout is empty, so do not run git against it: read the work through inspect_wright.\n", t.Branch, job.Site, t.DefaultBranch)
			if wt, err := sm.IntegrationWorktree(job.ID); err == nil {
				fmt.Fprintf(&w, "The branch is checked out at `%s`: push it from there, `git -C %s push origin %s`, only in the %s step.\n", wt, wt, t.Branch, OpenPRStep)
			}
			if site, err := r.st.GetSite(job.Site); err == nil {
				if repo, err := remote.Normalize(site.URL); err == nil {
					fmt.Fprintf(&w, "The Site's repository, for `gh -R`, is `%s`.\n", repo)
				}
			}
			w.WriteString("\n")
		}
	}
	w.WriteString("Accepted pieces; inspect_wright takes these names (not step ids or Input labels):\n" + strings.Join(accepted, "\n"))
	return w.String(), nil
}

// resumeNote tells a re-summoned Warden what the Wrights recorded so far.
func (r *Runs) resumeNote(sr store.StepRun) string {
	var w strings.Builder
	w.WriteString("\n## Resumed\n\nThe Works restarted while this step was running. Wrights still in flight are re-dispatched from their stored briefs; do not dispatch their pieces again. Pieces so far:\n")
	pieces, err := r.st.Pieces(sr.JobID)
	n := 0
	if err == nil {
		for _, p := range pieces {
			if p.Guild != sr.Guild {
				continue
			}
			n++
			state := "no dispatch"
			if d, err := r.st.LatestDispatch(p.JobID, p.Piece); err == nil {
				state = "latest dispatch " + d.Status
			}
			fmt.Fprintf(&w, "- %s: %s, %s\n", p.Piece, p.Status, state)
		}
	}
	if n == 0 {
		w.WriteString("- none\n")
	}
	return w.String()
}

// summon starts the Warden run for a running step. A step whose run cannot
// start is failed, so the Archwright can start it again.
func (r *Runs) summon(sr store.StepRun, resumed bool) error {
	r.mu.Lock()
	warden := r.warden
	r.mu.Unlock()
	fail := func(cause error) error {
		if err := r.st.FailStep(sr.JobID, sr.Step, "starting the run: "+cause.Error()); err != nil {
			log.Printf("step %s/%s: recording the failure: %v", sr.JobID, sr.Step, err)
		}
		return cause
	}
	root, err := r.spoolRootFor(sr.Guild)
	if err != nil {
		return fail(err)
	}
	brief := sr.Brief
	if resumed {
		brief += r.resumeNote(sr)
	}
	env, err := r.StartFor(RoleWarden, root, RunContext{Job: sr.JobID, Guild: sr.Guild, Task: sr.Step})
	if err != nil {
		return fail(err)
	}
	r.mu.Lock()
	closed := r.bg.Err() != nil
	if !closed {
		r.wg.Add(1) // under mu, so Close cannot Wait between the check and the Add
	}
	r.mu.Unlock()
	if closed {
		r.End(env.Run)
		return fail(fmt.Errorf("%w: the Works is shutting down", errInvalidInput))
	}
	if err := r.st.StartRunRecord(env.Run, sr.JobID, "", sr.Guild, "", sr.Step, ""); err != nil {
		log.Printf("run %s: recording it for the dashboard: %v", env.Run, err)
	}
	r.recordBrief(env.Run, brief)
	go func() {
		defer r.wg.Done()
		wt, runErr := r.wardenWorkspace(env.Run, root)
		var extra map[string]string
		if runErr == nil {
			extra, runErr = r.deliverGuildSkills(sr.Guild, env.Run, wt, nil)
		}
		if runErr == nil {
			runErr = warden.RunWarden(r.bg, WardenRequest{Env: env, Job: sr.JobID, Guild: sr.Guild, Step: sr.Step, Brief: brief, Resumed: resumed, Worktree: wt, ExtraEnv: extra, Trace: r.traceFor(env.Run)})
		}
		r.End(env.Run)
		r.endRunRecord(env.Run, runErr)
		if r.bg.Err() != nil {
			return // the Works is stopping: the step stays running and its Warden is re-summoned on the next start
		}
		msg := "the Warden ended without handing back"
		if runErr != nil {
			msg = "the Warden failed: " + runErr.Error()
		}
		if err := r.st.FailStep(sr.JobID, sr.Step, msg); err != nil { // a no-op once it handed back
			log.Printf("step %s/%s: recording the failure: %v", sr.JobID, sr.Step, err)
		}
	}()
	return nil
}

// wardenWorkspace is the directory a task Warden works in: a per-run
// directory the Works creates outside any Site, since a Warden leads through
// the Archwright tools and never touches code. It is a bare git repository
// root so Guild skills can be delivered into it and hidden from git like in
// a Wright's worktree.
func (r *Runs) wardenWorkspace(run, root string) (string, error) {
	r.mu.Lock()
	sd := r.skills
	r.mu.Unlock()
	base := filepath.Join(root, "warden-"+run)
	if sd != nil && sd.RunDir != nil {
		base = sd.RunDir(run)
	}
	dir := filepath.Join(base, "work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("preparing the Warden's directory: %w", err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		return "", fmt.Errorf("preparing the Warden's directory: git init: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return dir, nil
}

// endRunRecord notes how a Warden or reviewer run ended; a failure to record it is logged.
func (r *Runs) endRunRecord(run string, runErr error) {
	ended := "done"
	switch {
	case r.bg.Err() != nil:
		ended = "cancelled"
	case runErr != nil:
		ended = "failed"
	}
	if err := r.st.EndRunRecord(run, ended); err != nil {
		log.Printf("run %s: recording its end: %v", run, err)
	}
}

// resumeSteps re-summons a Warden for every step a stopped Works left running (ADR 0004).
func (r *Runs) resumeSteps() error {
	running, err := r.st.RunningSteps()
	if err != nil {
		return err
	}
	r.mu.Lock()
	warden := r.warden
	r.mu.Unlock()
	for _, sr := range running {
		job, err := r.st.GetJob(sr.JobID)
		if err != nil {
			continue
		}
		if job.Outcome != "" { // a closed Job's step has no Warden to resume; end it so it stops counting as active
			if err := r.st.FailStep(sr.JobID, sr.Step, "the Job closed while the step was running"); err != nil {
				log.Printf("step %s/%s: recording the failure: %v", sr.JobID, sr.Step, err)
			}
			continue
		}
		if warden == nil {
			log.Printf("step %s/%s: running, but no Warden runner is configured to resume it", sr.JobID, sr.Step)
			continue
		}
		if err := r.summon(sr, true); err != nil {
			log.Printf("step %s/%s: resuming: %v", sr.JobID, sr.Step, err)
		}
	}
	return nil
}
