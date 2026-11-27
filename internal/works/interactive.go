package works

import (
	"encoding/json"
	"fmt"
	"log"

	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

// interactiveBrief says whether a dispatch's stored brief declares an
// interactive Wright (dispatch_wright's interactive argument).
func interactiveBrief(brief json.RawMessage) bool {
	var b struct {
		Interactive bool `json:"interactive"`
	}
	return json.Unmarshal(brief, &b) == nil && b.Interactive
}

func interactiveKey(job, piece string) string {
	return store.NeedsInteractiveWright + ":" + job + ":" + piece
}

// paneAttach is the command that attaches a terminal to a Job's window of the
// factory and selects a Wright's pane in it.
func paneAttach(job, pane string) string {
	return fmt.Sprintf("tmux -L %s attach-session -t =%s:%s \\; select-pane -t %s", hosted.DefaultSocketName, hosted.DefaultSessionName, job, pane)
}

// AnnounceInteractive is the SessionRunner's Started hook: when an interactive
// Wright's Session starts, it files a Needs-you item naming the role, the Job,
// the pane and the command that attaches to it, and reports it to the
// Archwright so it tells the developer. One item stands for the Wright however
// many rounds it runs; reporting clears it.
func AnnounceInteractive(st *store.Store) func(RunRequest, hosted.Info) {
	return func(req RunRequest, info hosted.Info) {
		if !req.Interactive || req.Piece == "" || info.Pane == "" {
			return
		}
		attach := paneAttach(req.Job, info.Pane)
		_, err := st.AddNeedsYouReported(store.NewNeedsYou{
			Kind: store.NeedsInteractiveWright, JobID: req.Job, DedupeKey: interactiveKey(req.Job, req.Piece),
			Summary: fmt.Sprintf("Wright %s of Job %s is waiting for you in pane %s of window %s: attach with `%s`, then work with it there.",
				req.Piece, req.Job, info.Pane, req.Job, attach),
			Detail: map[string]string{"role": RoleWright, "guild": req.Guild, "piece": req.Piece, "job": req.Job, "window": req.Job, "pane": info.Pane, "attach": attach},
		}, "interactive_wright")
		if err != nil {
			log.Printf("dispatch %s: filing the attach item: %v", req.DispatchID, err)
		}
	}
}

// clearInteractive removes the attach item of a dispatch's Wright: it has reported.
func (r *Runs) clearInteractive(dispatchID string) {
	d, err := r.st.GetDispatch(dispatchID)
	if err != nil {
		return
	}
	if err := r.st.ResolveNeedsYou(interactiveKey(d.JobID, d.Piece)); err != nil {
		log.Printf("dispatch %s: clearing the attach item: %v", dispatchID, err)
	}
}
