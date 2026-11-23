package works

import (
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

func TestHandbackSummaryCarriesThePullRequestOfTheOpenPRStep(t *testing.T) {
	const pr = "https://github.com/o/r/pull/16"
	got := handbackSummary(store.Job{PR: pr}, OpenPRStep, "done", "masonry")
	if !strings.Contains(got, "Step open-pr handed back done by the masonry Warden") || !strings.Contains(got, "Pull request: "+pr) {
		t.Fatalf("summary: %q", got)
	}
	if got := handbackSummary(store.Job{PR: pr}, "review", "done", "hallmark"); strings.Contains(got, pr) {
		t.Fatalf("a step other than open-pr should not carry the PR: %q", got)
	}
	if got := handbackSummary(store.Job{}, OpenPRStep, "failed", "masonry"); strings.Contains(got, "Pull request") {
		t.Fatalf("no PR recorded, none to report: %q", got)
	}
}
