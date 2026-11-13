package works

import (
	"fmt"

	"github.com/phallatlassian/archwright/internal/runner"
	"github.com/phallatlassian/archwright/internal/store"
)

// RaisePermissionsWanted files a "permission wanted" Needs-you item for each
// action a run's runner denied because it would have prompted (ADR 0011). The
// developer fixes it in their own permission rules and the Warden re-dispatches,
// or skips it; the factory never approves anything itself. It reports how many
// items were new.
func RaisePermissionsWanted(st *store.Store, jobID, site string, denials []runner.Denial) (int, error) {
	added := 0
	for _, d := range denials {
		ok, err := st.AddNeedsYou(store.NewNeedsYou{
			Kind: store.NeedsPermissionWanted, JobID: jobID, Site: site,
			DedupeKey: store.NeedsPermissionWanted + ":" + jobID + ":" + d.Tool + ":" + d.Command,
			Summary:   fmt.Sprintf("Permission wanted: %s was denied: %s", d.Tool, d.Command),
			Detail:    map[string]any{"tool": d.Tool, "command": d.Command, "tool_use_id": d.ToolUseID},
		})
		if err != nil {
			return added, err
		}
		if ok {
			added++
		}
	}
	return added, nil
}
