package doctor

import (
	"fmt"

	"github.com/phallatlassian/archwright/internal/store"
)

// checkTrust reports what each Site's trust probe found (docs/spec/tmux-runtime.md):
// whether a Session of each runner starts in a worktree of the Site, which needs
// folder trust (and Codex's hook trust) and shows the vendor's hooks reach the
// Works. A Site whose dialog is unanswered is a warning: its Sessions would
// stall at the dialog with no signal, but runs of other Sites are not blocked.
func checkTrust(r *collector, snap *store.Snapshot) {
	if snap == nil {
		return
	}
	sites, err := snap.Sites()
	if err != nil || len(sites) == 0 {
		return // reported by the Sites check
	}
	all, err := snap.SiteTrusts("")
	if err != nil {
		r.warn("trust", "cannot read the trust probes' results: "+err.Error(), "re-run `archwright doctor`")
		return
	}
	bySite := map[string][]store.SiteTrust{}
	for _, t := range all {
		bySite[t.Site] = append(bySite[t.Site], t)
	}
	for _, s := range sites {
		probes := bySite[s.Name]
		if len(probes) == 0 {
			r.warn("trust:"+s.Name, "no trust probe has run for this Site, so a Session in it may stall at a trust dialog with no signal",
				fmt.Sprintf("run it: archwright site probe %s (registering a Site does this; this one was registered before, or the Works hosts no Sessions)", s.Name))
			continue
		}
		for _, p := range probes {
			name := "trust:" + s.Name + ":" + p.Runner
			switch p.State {
			case store.TrustCaptured:
				r.pass(name, "%s", p.Detail)
			case store.TrustUnavailable:
				r.pass(name, "%s", p.Detail)
			case store.TrustPending:
				r.warn(name, "the trust probe is still waiting: "+p.Detail,
					fmt.Sprintf("answer the dialog in the probe's window (see `archwright needs-you list`), or re-run it later: archwright site probe %s", s.Name))
			default: // unconfirmed, failed
				r.warn(name, p.Detail, trustFix(s.Name, p.Runner))
			}
		}
	}
}

func trustFix(site, runner string) string {
	what := "answer " + runner + "'s folder-trust dialog"
	if runner == "codex" {
		what += " and its hook-trust dialog (\"Trust all and continue\")"
	}
	return fmt.Sprintf("attach to the probe Session (tmux -L archwright attach-session -t =factory:probe-%s), %s once, then re-check with `archwright site probe %s`; "+
		"the factory never answers it or writes trust configuration for you. If no dialog appears, your managed policy may block hooks: Sessions then run with state read from the pane, marked unreliable", site, what, site)
}
