package works

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/store"
)

// mergePiecePiece prefixes the pieces accept_piece dispatches to merge a
// conflicting piece; each is numbered.
const mergePiecePiece = "merge-piece"

// isMergeWright is a piece a merge Wright works on, in the integration worktree.
func isMergeWright(piece string) bool { return strings.HasPrefix(piece, mergePiecePiece+"-") }

// Statuses of an AcceptInfo.
const (
	AcceptAccepted = "accepted"
	AcceptConflict = "conflict"    // the piece conflicts with the integration branch: a merge Wright was started, the piece is still open
	AcceptMerging  = "in_progress" // a merge Wright is already working on the integration branch; nothing more was started
)

// AcceptInfo is the answer to accept_piece. The piece's own fields come first;
// Merge says what became of its branch: merged, unchanged (it had no commits
// the integration branch lacks) or conflict.
type AcceptInfo struct {
	store.Piece
	Merge     string        `json:"merge,omitempty"`
	Conflicts []string      `json:"conflicts,omitempty"`
	Message   string        `json:"message,omitempty"`
	Dispatch  *DispatchInfo `json:"dispatch,omitempty"`
}

// acceptPiece accepts a Wright's finished piece and lands its branch on the
// Job's integration branch, the branch the gates, the push and the PR use
// (ADR 0028): a fast-forward or merge commit made by the Works, with no agent
// run. A piece with no commits is accepted and changes nothing. A conflict is
// never left half-merged: the integration branch stays as it was, the piece
// stays open and a merge Wright is dispatched to merge it; the Warden accepts
// that merge piece, then calls accept_piece on this one again.
func acceptPiece(c *callCtx, in json.RawMessage) (any, error) {
	acceptMu.Lock() // one merge into a Job's integration branch at a time, however many calls race
	defer acceptMu.Unlock()
	var a struct {
		Piece string `json:"piece"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	g, err := c.warden()
	if err != nil {
		return nil, err
	}
	p, err := c.pieceOf(a.Piece)
	if err != nil {
		return nil, err
	}
	d, err := c.st.LatestDispatch(p.JobID, p.Piece)
	if err != nil {
		return nil, err
	}
	var out AcceptInfo
	c.runs.mu.Lock()
	sm := c.runs.sites
	c.runs.mu.Unlock()
	// only a piece that can be accepted is merged; a merge Wright's piece commits on the integration branch itself
	if sm != nil && d.Branch != "" && p.Status == store.PieceOpen && d.Status == store.DispatchReported && resultIsDone(d.Result) {
		if err := c.jobRunnable(); err != nil {
			return nil, err
		}
		if md, err := c.activeMergeWright(p.JobID); err != nil {
			return nil, err
		} else if md != nil {
			return c.mergingInfo(p, md, out), nil
		}
		if err := sm.EnsureIntegration(p.JobID); err != nil {
			return nil, err
		}
		pm, err := sm.MergePiece(p.JobID, d.Branch)
		if err != nil {
			return nil, err
		}
		out.Merge = pm.Status
		if pm.Status == sites.PieceConflict {
			return c.conflictingPiece(g, p, d, pm, out)
		}
	}
	if err := c.st.AcceptPiece(p.JobID, p.Piece, resultIsDone); err != nil {
		return nil, err
	}
	if out.Piece, err = c.st.GetPiece(p.JobID, p.Piece); err != nil {
		return nil, err
	}
	return out, nil
}

// conflictingPiece dispatches the merge Wright for a piece whose branch does
// not merge cleanly; accept_piece has already ruled out a running one.
func (c *callCtx) conflictingPiece(g Guild, p store.Piece, d store.Dispatch, pm sites.PieceMerge, out AcceptInfo) (any, error) {
	out.Piece, out.Merge, out.Conflicts = p, sites.PieceConflict, pm.Conflicts
	pieces, err := c.st.Pieces(p.JobID)
	if err != nil {
		return nil, err
	}
	n := 1
	for _, mp := range pieces {
		if strings.HasPrefix(mp.Piece, mergePiecePiece+"-") {
			n++
		}
	}
	b := pieceMergeBrief(p.Piece, d.Branch, pm)
	raw, _ := json.Marshal(b)
	piece := fmt.Sprintf("%s-%d", mergePiecePiece, n)
	md, err := c.st.NewPiece(p.JobID, g.Name, piece, c.run.id, raw, b.Render(g.Name, g.Standards, g.Skills, 0, ""))
	if err != nil {
		return nil, err
	}
	pos, err := c.runs.launch(md)
	if err != nil {
		return nil, err
	}
	info := DispatchInfo{DispatchID: md.ID, Piece: md.Piece, Round: md.Round, Status: store.DispatchRunning}
	if pos > 0 {
		info.Status, info.QueuePos = store.DispatchQueued, pos
	}
	out.Status, out.Dispatch = AcceptConflict, &info
	out.Message = fmt.Sprintf("Piece %q conflicts with the integration branch (%s); nothing was merged and the piece is still open. A merge Wright was started as piece %q: inspect_wright it, accept it, then accept %q again.", p.Piece, strings.Join(pm.Conflicts, ", "), piece, p.Piece)
	return out, nil
}

// activeMergeWright is the merge Wright of either kind that holds the Job's
// integration worktree (queued, running or blocked), or nil.
func (c *callCtx) activeMergeWright(jobID string) (*store.Dispatch, error) {
	pieces, err := c.st.Pieces(jobID)
	if err != nil {
		return nil, err
	}
	for _, mp := range pieces {
		if !isMergeWright(mp.Piece) {
			continue
		}
		if md, err := c.st.LatestDispatch(jobID, mp.Piece); err == nil && (md.Status == store.DispatchQueued || md.Status == store.DispatchRunning || md.Status == store.DispatchBlocked) {
			return &md, nil
		}
	}
	return nil, nil
}

// mergingInfo answers accept_piece while a merge Wright holds the integration
// worktree: nothing is merged or started, the piece stays as it was.
func (c *callCtx) mergingInfo(p store.Piece, md *store.Dispatch, out AcceptInfo) AcceptInfo {
	info := DispatchInfo{DispatchID: md.ID, Piece: md.Piece, Round: md.Round, Status: md.Status}
	out.Piece, out.Status, out.Dispatch = p, AcceptMerging, &info
	out.Message = fmt.Sprintf("Merge Wright piece %q is working on the integration branch; nothing was merged. inspect_wright it, accept it, then accept %q again.", md.Piece, p.Piece)
	return out
}

// pieceMergeBrief is the brief of the merge Wright for a conflicting piece.
// The Works writes it, not the Warden, so the Wright is held to a merge.
func pieceMergeBrief(piece, branch string, pm sites.PieceMerge) Brief {
	var b Brief
	b.Objective = fmt.Sprintf("Piece %q is accepted but its branch %s conflicts with the integration branch %s. Merge %s into %s, resolve every conflict so that both sides' intent survives, and commit the merge.", piece, branch, pm.Branch, branch, pm.Branch)
	b.Scope.In = []string{"merging " + branch + " into " + pm.Branch + " in your working directory, which is already on " + pm.Branch, "resolving the conflicts that merge raises"}
	b.Scope.Out = []string{"any change that is not needed to resolve a conflict", "pushing, fetching or any other use of the network"}
	cons := []string{
		"Run `git merge --no-edit " + branch + "` and resolve the conflicts it reports; the branch is already in this repository.",
		"Never rebase, reset, amend, cherry-pick, squash, or otherwise rewrite history; never force anything. Only add the merge commit on top of the branch as it is.",
		"Never run `git push`, `git fetch` or `gh`: this merge only joins two branches that are already in this repository.",
		"Run one simple command per Bash call, with no pipes, heredocs, `cd` or command substitution.",
		"If a conflict cannot be resolved without a decision that is not yours, run `git merge --abort`, then ask.",
	}
	b.Constraints = &cons
	b.Inputs = []string{"branch to merge: " + branch, "integration branch: " + pm.Branch, "integration tip before the merge: " + pm.Tip, "conflicted files: " + strings.Join(pm.Conflicts, ", ")}
	b.DefinitionOfDone = []string{"git log shows a merge commit on " + pm.Branch + " whose parents are " + pm.Tip + " and the tip of " + branch, "no conflict markers remain and the working tree is clean", "the merged code still builds and the Site's tests still pass where they can be run"}
	b.ReturnShape = "the merge commit's SHA and a line for each conflicted file saying how it was resolved"
	b.Limits.TimeoutMinutes = 30
	return b
}

// acceptMu serialises accept_piece so two racing calls cannot merge, or start a merge Wright, at once.
var acceptMu sync.Mutex
