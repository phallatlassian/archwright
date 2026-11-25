package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/phallatlassian/archwright/internal/forge"
	"github.com/phallatlassian/archwright/internal/store"
)

const (
	installGH = "install the GitHub CLI so `gh` is on PATH (macOS: brew install gh; elsewhere https://cli.github.com), then sign in with `gh auth login`"
	signInGH  = "sign in yourself with `gh auth login` (the factory never signs in for you or stores a credential); then re-run `archwright doctor`"
)

// checkForge checks the developer's gh, which agents open PRs with and the
// Works reads their state through (ADR 0028): installed, then signed in. It
// reads only (`gh auth status`) and prints the remedy; it never installs or
// signs in. Whether an agent's `gh` and `git push` prompt is checked by
// checkPrompts.
//
// gh blocks runs only while a registered Site is on a host the Works has a
// GitHub adapter for. A Site on another host still works locally, and with no
// such Site a gh problem is only a warning.
func checkForge(ctx context.Context, r *collector, d Deps, snap *store.Snapshot) {
	needers := forgeSites(snap)
	problem := func(name, detail, fix string) {
		if len(needers) > 0 {
			r.fail(name, fmt.Sprintf("%s; Site(s) %s open PRs and read their state through it", detail, strings.Join(needers, ", ")), fix)
			return
		}
		r.warn(name, detail+" (no registered Site is on GitHub, so runs are not blocked)", fix)
	}

	bin, err := d.LookPath("gh")
	if err != nil {
		problem("forge:gh", "`gh` is not installed or not on PATH, so agents cannot open PRs on GitHub and the Works cannot read their state", installGH)
		return
	}
	r.pass("forge:gh", "%s", bin)

	out, err := d.Output(ctx, bin, "auth", "status", "--hostname", "github.com")
	if err == nil {
		r.pass("forge:gh-auth", "gh is signed in to github.com")
		return
	}
	switch fault := forge.Classify(out, err); fault.Kind {
	case forge.KindAuth:
		problem("forge:gh-auth", "gh is not signed in to github.com: "+firstLine(out), signInGH)
	case forge.KindNetwork:
		r.warn("forge:gh-auth", "could not verify gh's sign-in, the network did not answer: "+firstLine(out),
			"check your network, then re-run `archwright doctor`; runs are not blocked on this")
	default:
		problem("forge:gh-auth", fmt.Sprintf("`gh auth status` failed: %s", firstLine(out)), "run `gh auth status` yourself and repair the install or sign in with `gh auth login`")
	}
}

// forgeSites names the registered Sites whose remote the Works can read PRs
// of; none when the registry is unreadable or empty.
func forgeSites(snap *store.Snapshot) []string {
	if snap == nil {
		return nil
	}
	sites, err := snap.Sites()
	if err != nil {
		return nil
	}
	var names []string
	for _, s := range sites {
		if forge.SupportsSite(s.URL) {
			names = append(names, s.Name)
		}
	}
	return names
}
