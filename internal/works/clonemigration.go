package works

import (
	"fmt"
	"log"

	"github.com/phallatlassian/archwright/internal/sites"
	"github.com/phallatlassian/archwright/internal/store"
)

// EnvMigrateClones turns on migrating Sites' bare clones at Works start. ADR
// 0030 keeps it off until the live check (#191) has shown that both CLIs
// inherit folder trust into worktrees of a normal clone: the migration costs
// the developer a trust prompt per runner, which is wasted if they do not.
const EnvMigrateClones = "ARCHWRIGHT_MIGRATE_CLONES"

// MigrateClonesEnabled reports whether the environment turns the migration on.
func MigrateClonesEnabled(getenv func(string) string) bool {
	switch getenv(EnvMigrateClones) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// CloneMigrationKey is the dedupe key of the Needs-you item saying a Site's
// migration is waiting for its Jobs to close.
func CloneMigrationKey(site string) string { return store.NeedsCloneMigration + ":" + site }

// MigrateClones replaces each Site's bare clone with a normal clone with a main
// checkout (ADR 0030) and starts the trust probe in the new main checkout, so
// the developer is asked once per runner. A Site with an active Job is not
// migrated: a Needs-you item says the migration is waiting, and it is tried
// again at the next start. It reports the Sites it migrated.
func (r *Runs) MigrateClones() []string {
	r.mu.Lock()
	sm := r.sites
	r.mu.Unlock()
	if sm == nil {
		return nil
	}
	list, err := r.st.ListSites()
	if err != nil {
		log.Printf("migrating clones: %v", err)
		return nil
	}
	var done []string
	for _, s := range list {
		migrated, err := sm.Migrate(s.Name)
		switch {
		case sites.IsBlocked(err):
			r.migrationWaiting(s.Name, err)
		case err != nil:
			log.Printf("migrating the clone of Site %s: %v", s.Name, err)
		case migrated:
			r.st.ResolveNeedsYou(CloneMigrationKey(s.Name))
			log.Printf("Site %s: replaced its bare clone with a normal clone; asking the developer to trust its main checkout once per runner", s.Name)
			r.ProbeSite(s.Name)
			done = append(done, s.Name)
		default:
			r.st.ResolveNeedsYou(CloneMigrationKey(s.Name))
		}
	}
	return done
}

func (r *Runs) migrationWaiting(site string, cause error) {
	_, err := r.st.AddNeedsYou(store.NewNeedsYou{
		Kind: store.NeedsCloneMigration, Site: site, DedupeKey: CloneMigrationKey(site),
		Summary: fmt.Sprintf("Site %s still has its bare clone: its move to a normal clone with one trusted main checkout (ADR 0030) is waiting for its active Jobs to close. "+
			"It runs at the next Works start after they have, and asks you to trust the main checkout once per runner.", site),
		Detail: map[string]string{"waiting_for": cause.Error()},
	})
	if err != nil {
		log.Printf("filing the clone migration item of Site %s: %v", site, err)
	}
}
