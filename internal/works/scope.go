package works

import (
	"encoding/json"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// scopePath normalises a declared scope entry to a repository-relative path;
// "" means the whole repository.
func scopePath(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "*?["); i >= 0 {
		s = s[:strings.LastIndex(s[:i], "/")+1]
	}
	s = strings.Trim(path.Clean("/"+s), "/")
	return s
}

// scopesOverlap reports whether two scope entries cover any common path.
func scopesOverlap(a, b string) bool {
	a, b = scopePath(a), scopePath(b)
	return a == "" || b == "" || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// warnScopeOverlap files a Needs-you item for each other open Job on the same
// Site whose writing scope overlaps this dispatch's. It never blocks the
// dispatch: concurrent Jobs are allowed and conflicts surface at merge time
// (ADR 0008).
func (r *Runs) warnScopeOverlap(d store.Dispatch, b Brief) {
	if b.ReadOnly {
		return
	}
	job, err := r.st.GetJob(d.JobID)
	if err != nil || job.Site == "" {
		return
	}
	others, err := r.st.SiteBriefs(job.Site, job.ID)
	if err != nil {
		log.Printf("dispatch %s: scope overlap check: %v", d.ID, err)
		return
	}
	type pair struct {
		Mine   string `json:"this_job"`
		Theirs string `json:"other_job"`
	}
	overlaps := map[string][]pair{}
	for _, o := range others {
		var ob Brief
		if json.Unmarshal(o.Brief, &ob) != nil || ob.ReadOnly {
			continue
		}
		for _, mine := range b.Scope.In {
			for _, theirs := range ob.Scope.In {
				if !blank(mine) && !blank(theirs) && scopesOverlap(mine, theirs) {
					overlaps[o.JobID] = append(overlaps[o.JobID], pair{mine, theirs})
				}
			}
		}
	}
	ids := make([]string, 0, len(overlaps))
	for id := range overlaps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, other := range ids {
		ps := overlaps[other]
		lo, hi := job.ID, other
		if hi < lo {
			lo, hi = hi, lo
		}
		var shown []string
		for _, p := range ps {
			shown = append(shown, fmt.Sprintf("%s ~ %s", p.Mine, p.Theirs))
		}
		sort.Strings(shown)
		_, err := r.st.AddNeedsYou(store.NewNeedsYou{
			Kind: store.NeedsScopeOverlap, JobID: job.ID, Site: job.Site,
			Summary: fmt.Sprintf("Jobs %s and %s both write to overlapping scope on Site %s (%s); both continue, expect conflicts at merge time",
				job.ID, other, job.Site, shown[0]),
			DedupeKey: strings.Join(append([]string{store.NeedsScopeOverlap, lo, hi}, shown...), "|"),
			Detail:    map[string]any{"jobs": []string{job.ID, other}, "site": job.Site, "overlaps": ps},
		})
		if err != nil {
			log.Printf("dispatch %s: filing scope overlap warning: %v", d.ID, err)
		}
	}
}
