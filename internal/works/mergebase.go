package works

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/store"
)

func init() {
	catalogue["merge_base"] = tool{roles: []string{RoleWarden}, call: mergeBase}
}

// mergeBasePiece prefixes the pieces merge_base dispatches; each is numbered.
const mergeBasePiece = "merge-base"

// Mergeable values of a pull request as the Forge reports them.
const mergeConflicting = "CONFLICTING"

func isMergeBasePiece(piece string) bool { return strings.HasPrefix(piece, mergeBasePiece+"-") }

// Statuses of a BaseMerge answer.
const (
	BaseMergeDispatched = "dispatched"  // a merge Wright was started
	BaseMergeRunning    = "in_progress" // one is already running; nothing more was started
	BaseMergeNoConflict = "no_conflict" // the PR read shows no conflict; nothing was started
	BaseMergeMerged     = "merged"      // the base is already in the integration branch; push_branch it
)

// BaseMergeInfo is the answer to merge_base.
type BaseMergeInfo struct {
	Status    string        `json:"status"`
	Mergeable string        `json:"mergeable,omitempty"`
	Message   string        `json:"message,omitempty"`
	Dispatch  *DispatchInfo `json:"dispatch,omitempty"`
}

// mergeBase resolves a conflict between the Job's PR and its base branch the
// way ADR 0022 says: it reads the PR to see the conflict, fetches the base
// into the Works' clone, and dispatches a merge Wright into the Job's
// integration worktree to merge the base into the integration branch and
// commit. The Warden inspects and accepts that piece like any other and then
// calls push_branch, which pushes the new commits as a fast-forward on the
// same PR. Nothing here rebases, resets or forces, and it takes no argument:
// the branch, base and PR are all the Job's.
func mergeBase(c *callCtx, in json.RawMessage) (any, error) {
	mergeBaseMu.Lock() // one merge Wright per Job, however many calls race
	defer mergeBaseMu.Unlock()
	if err := decode(in, &struct{}{}); err != nil {
		return nil, err
	}
	g, err := c.warden()
	if err != nil {
		return nil, err
	}
	if err := c.jobRunnable(); err != nil {
		return nil, err
	}
	job, err := c.forgeJob()
	if err != nil {
		return nil, err
	}
	prURL, err := jobPR(job)
	if err != nil {
		return nil, err
	}
	pieces, err := c.st.Pieces(job.ID)
	if err != nil {
		return nil, err
	}
	n := 1
	for _, p := range pieces {
		if !isMergeBasePiece(p.Piece) {
			continue
		}
		n++
		d, err := c.st.LatestDispatch(job.ID, p.Piece)
		if err == nil && (d.Status == store.DispatchQueued || d.Status == store.DispatchRunning || d.Status == store.DispatchBlocked) {
			info := DispatchInfo{DispatchID: d.ID, Piece: d.Piece, Round: d.Round, Status: d.Status}
			return BaseMergeInfo{Status: BaseMergeRunning, Message: "A merge Wright is already working on this Job; inspect_wright it.", Dispatch: &info}, nil
		}
	}
	pr, err := c.runs.forgeOrDefault().ReadPR(c.runs.bg, prURL)
	if err != nil {
		return nil, c.runs.forgeFailure(job, "read the pull request", err)
	}
	if !strings.EqualFold(pr.Mergeable, mergeConflicting) {
		return BaseMergeInfo{Status: BaseMergeNoConflict, Mergeable: pr.Mergeable,
			Message: "The pull request does not show a merge conflict (UNKNOWN means the Forge has not worked it out yet: read_pr again shortly); nothing was merged."}, nil
	}
	c.runs.mu.Lock()
	sm := c.runs.sites
	c.runs.mu.Unlock()
	if sm == nil {
		return nil, fmt.Errorf("%w: this Job has no Site, so there is no branch to merge into", errInvalidInput)
	}
	bm, err := sm.PrepareBaseMerge(job.ID)
	if err != nil {
		return nil, err
	}
	if bm.Merged {
		return BaseMergeInfo{Status: BaseMergeMerged, Mergeable: pr.Mergeable,
			Message: "The base is already merged into the integration branch; the Forge may not have caught up. Call push_branch, then read_pr."}, nil
	}
	b := baseMergeBrief(bm)
	raw, _ := json.Marshal(b)
	piece := fmt.Sprintf("%s-%d", mergeBasePiece, n)
	d, err := c.st.NewPiece(job.ID, g.Name, piece, c.run.id, raw, b.Render(g.Name, g.Standards, g.Skills, 0, ""))
	if err != nil {
		return nil, err
	}
	pos, err := c.runs.launch(d)
	if err != nil {
		return nil, err
	}
	info := DispatchInfo{DispatchID: d.ID, Piece: d.Piece, Round: d.Round, Status: store.DispatchRunning}
	if pos > 0 {
		info.Status, info.QueuePos = store.DispatchQueued, pos
	}
	return BaseMergeInfo{Status: BaseMergeDispatched, Mergeable: pr.Mergeable, Dispatch: &info}, nil
}

// baseMergeBrief is the brief of the merge Wright. The Works writes it, not
// the Warden, so the Wright is held to a merge and nothing else.
func baseMergeBrief(bm sites.BaseMerge) Brief {
	var b Brief
	b.Objective = fmt.Sprintf("The pull request for this Job conflicts with its base branch. Merge %s (commit %s) into the integration branch %s, resolve every conflict so that both sides' intent survives, and commit the merge.", bm.BaseRef, bm.BaseSHA, bm.Branch)
	b.Scope.In = []string{"merging " + bm.BaseRef + " into " + bm.Branch + " in your working directory, which is already on " + bm.Branch, "resolving the conflicts that merge raises"}
	b.Scope.Out = []string{"any change that is not needed to resolve a conflict", "pushing, fetching or any other use of the network"}
	cons := []string{
		"Run `git merge --no-edit " + bm.BaseRef + "` and resolve the conflicts it reports; the base is already fetched.",
		"Never rebase, reset, amend, cherry-pick, squash, or otherwise rewrite history; never force anything. Only add the merge commit on top of the branch as it is. The Works pushes the result as a fast-forward.",
		"Never run `git push`, `git fetch` or `gh`; the Works does that.",
		"Run one simple command per Bash call, with no pipes, heredocs, `cd` or command substitution.",
		"If a conflict cannot be resolved without a decision that is not yours, run `git merge --abort`, then ask.",
	}
	b.Constraints = &cons
	b.Inputs = []string{"base ref: " + bm.BaseRef, "base commit: " + bm.BaseSHA, "integration branch: " + bm.Branch, "integration tip before the merge: " + bm.Tip}
	b.DefinitionOfDone = []string{"git log shows a merge commit on " + bm.Branch + " whose parents are " + bm.Tip + " and " + bm.BaseSHA, "no conflict markers remain and the working tree is clean", "the merged code still builds and the Site's tests still pass where they can be run"}
	b.ReturnShape = "the merge commit's SHA and a line for each conflicted file saying how it was resolved"
	b.Limits.TimeoutMinutes = 30
	return b
}

// mergeBaseMu serialises merge_base so two racing calls cannot both start a merge Wright.
var mergeBaseMu sync.Mutex
