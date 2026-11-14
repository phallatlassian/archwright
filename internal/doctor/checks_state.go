package doctor

import (
	"errors"
	"fmt"
	"os"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/launch"
	"github.com/phallatlassian/archwright/internal/skills"
	"github.com/phallatlassian/archwright/internal/store"
)

// checkConfig reads config.toml and returns the chosen runner for the Archwright.
func checkConfig(r *collector, d Deps) string {
	chosen, err := launch.ReadRunner(d.Dirs)
	if err != nil {
		r.fail("config", err.Error(), fmt.Sprintf(`edit %s so it says: runner = "claude" (or "codex")`, launch.ConfigFile(d.Dirs)))
		return launch.RunnerClaude
	}
	r.pass("config", "the Archwright runs on %s (%s)", chosen, launch.ConfigFile(d.Dirs))
	return chosen
}

// checkDirs wants the config and state directories, and the database, private to the user.
func checkDirs(r *collector, d Deps) {
	for _, dir := range []struct{ name, path string }{{"config", d.Dirs.Config}, {"state", d.Dirs.State}} {
		name := "directory:" + dir.name
		fi, err := os.Stat(dir.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			r.pass(name, "%s does not exist yet; the Works creates it (mode 0700) on first use", dir.path)
		case err != nil:
			r.fail(name, err.Error(), "make "+dir.path+" readable: it is the per-user directory (ADR 0018)")
		case !fi.IsDir():
			r.fail(name, dir.path+" is not a directory", "move it aside: mv "+dir.path+" "+dir.path+".old")
		case fi.Mode().Perm() != 0o700:
			r.fail(name, fmt.Sprintf("%s has mode %04o, want 0700", dir.path, fi.Mode().Perm()), "chmod 700 "+dir.path)
		default:
			r.pass(name, "%s (mode 0700)", dir.path)
		}
	}
	db := d.Dirs.Database()
	if fi, err := os.Stat(db); err == nil && fi.Mode().Perm()&0o077 != 0 {
		r.fail("directory:database", fmt.Sprintf("%s has mode %04o, want 0600", db, fi.Mode().Perm()), "chmod 600 "+db)
	}
}

// checkSchema compares the database's schema version with this binary's. It
// reads a private copy of the database (store.OpenSnapshot), never the file
// itself, and returns it when its Sites can be read.
func checkSchema(r *collector, d Deps) *store.Snapshot {
	db := d.Dirs.Database()
	snap, err := store.OpenSnapshot(db)
	if errors.Is(err, os.ErrNotExist) {
		r.pass("schema", "no database yet; the Works creates it at schema v%d on first use", store.SchemaVersion())
		return nil
	}
	if err != nil {
		r.fail("schema", "cannot read the database: "+err.Error(), "if the file is damaged, restore the copy the Works made before its last migration, next to "+db)
		return nil
	}
	want := store.SchemaVersion()
	switch {
	case snap.Version > want:
		r.fail("schema", fmt.Sprintf("the database is schema v%d, this binary supports v%d, and a binary refuses to open a newer schema", snap.Version, want),
			"rebuild archwright from the current source (git pull && make build, then reinstall it), then run `archwright works status`")
		snap.Close()
		return nil
	case snap.Version < want:
		r.pass("schema", "the database is schema v%d; the Works migrates it to v%d at its next start, copying it aside first", snap.Version, want)
		r.warn("sites", "Site checks are skipped: the registry is still at the old schema", "start the Works to migrate it (`archwright site list` does), then re-run `archwright doctor`")
		snap.Close()
		return nil
	}
	r.pass("schema", "schema v%d, current", want)
	return snap
}

// checkWorks reports reachability without starting the Works.
func checkWorks(r *collector, d Deps) {
	if pid, port, ok := d.WorksRunning(); ok {
		r.pass("works", "reachable: pid %d, 127.0.0.1:%d", pid, port)
		return
	}
	if _, err := os.Stat(d.Dirs.PidFile()); err == nil {
		r.warn("works", "a pid file exists but nothing answers on it; the Works died",
			"nothing to do: the next command starts it again and recovers (ADR 0002); to clear it now run `archwright stop`, then any command")
		return
	}
	r.pass("works", "not running; any command starts it on demand")
}

// checkSkills verifies the vendored skills and that the Guilds' definitions
// load and name only skills the catalogue has.
func checkSkills(r *collector, d Deps) {
	cat := skills.Vendored()
	if err := cat.Verify(); err != nil {
		r.fail("skills", err.Error(), "restore the snapshot: git checkout -- internal/skills/vendor, or re-vendor, then rebuild archwright")
		return
	}
	reg := defs.New(defs.Shipped(), d.Dirs.Config)
	for _, e := range reg.Reload() {
		r.fail("skills", "a "+e.Kind+" definition did not load: "+e.Error(), "fix or remove "+e.Source+", then run `archwright catalogue reload`")
		return
	}
	for _, g := range reg.Guilds() {
		if _, err := cat.Select(g.Skills); err != nil {
			r.fail("skills", fmt.Sprintf("Guild %q: %v", g.Name, err), fmt.Sprintf("remove the unknown skill from the Guild's skills list (%s), or vendor it", g.Source))
			return
		}
	}
	r.pass("skills", "vendored skills match their provenance; %d Guilds and %d Jigs load", len(reg.Guilds()), len(reg.Jigs()))
}

// checkLavish: Lavish is a convenience, never a dependency (ADR 0015), so its
// absence is a warning only.
func checkLavish(r *collector, d Deps) {
	if p, err := d.LookPath("lavish-axi"); err == nil {
		r.pass("lavish-axi", "%s", p)
		return
	}
	r.warn("lavish-axi", "not installed; asks fall back to plain text in the session (runs are not blocked)",
		"install it so asks open as pages: npm install -g lavish-axi")
}
