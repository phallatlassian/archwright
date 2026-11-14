package works

import "github.com/phallatlassian/archwright/internal/store"

// Job board states, in the order a Job is placed: the first that holds wins.
const (
	BoardAccepted  = "accepted"
	BoardDiscarded = "discarded"
	BoardParked    = "parked"      // waiting on the Archwright after a partial handback or a second failed review
	BoardNeedsYou  = "needs-you"   // a Plan to approve, or a blocking ask the developer must answer
	BoardPaused    = "paused"      // the developer paused it
	BoardRunning   = "running"     // a Wright or Review is queued or running
	BoardPlanning  = "planning"    // no Plan yet
	BoardIdle      = "in-progress" // between steps: waiting for the Archwright's next move
)

// BoardJob is one Job on the dashboard's board, with the state it is placed by.
type BoardJob struct {
	store.Job
	State        string  `json:"state"`
	Approval     string  `json:"plan_approval,omitempty"`
	Jig          string  `json:"jig,omitempty"`
	Steps        int     `json:"steps"`
	LiveRuns     int     `json:"live_runs"`
	OpenAsks     int     `json:"open_asks"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
}

// Board lists every Job across Sites with the state it sits in.
func Board(st *store.Store) ([]BoardJob, error) {
	jobs, err := st.ListJobs()
	if err != nil {
		return nil, err
	}
	usage, err := st.Usage()
	if err != nil {
		return nil, err
	}
	type total struct {
		in, out int
		cost    float64
	}
	spent := map[string]total{}
	for _, u := range usage {
		t := spent[u.JobID]
		t.in, t.out, t.cost = t.in+u.InputTokens, t.out+u.OutputTokens, t.cost+u.CostUSD
		spent[u.JobID] = t
	}
	out := make([]BoardJob, 0, len(jobs))
	for _, j := range jobs {
		full, err := jobWithPlan(st, j.ID)
		if err != nil {
			return nil, err
		}
		b := BoardJob{Job: j, OpenAsks: len(full.Asks)}
		if full.Plan != nil {
			b.Approval, b.Jig, b.Steps = full.Plan.Approval, full.Plan.Jig, len(full.Plan.Steps)
		}
		ds, err := st.Dispatches(j.ID)
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			if d.Status == store.DispatchQueued || d.Status == store.DispatchRunning {
				b.LiveRuns++
			}
		}
		rs, err := st.Reviews(j.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if r.Status == store.ReviewRunning {
				b.LiveRuns++
			}
		}
		t := spent[j.ID]
		b.InputTokens, b.OutputTokens, b.CostUSD = t.in, t.out, t.cost
		b.State = boardState(b, full)
		out = append(out, b)
	}
	return out, nil
}

func boardState(b BoardJob, full JobWithPlan) string {
	switch {
	case b.Outcome == "accepted":
		return BoardAccepted
	case b.Outcome != "":
		return BoardDiscarded
	case b.ParkedFor != "":
		return BoardParked
	case b.Approval == store.ApprovalPending || full.BlockedOnYou:
		return BoardNeedsYou
	case b.PausedAt != "":
		return BoardPaused
	case b.LiveRuns > 0:
		return BoardRunning
	case full.Plan == nil:
		return BoardPlanning
	}
	return BoardIdle
}
