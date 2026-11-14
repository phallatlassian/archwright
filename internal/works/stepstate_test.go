package works

import (
	"testing"

	"github.com/phallatlassian/archwright/internal/store"
)

func TestStepStatesAreHonestAboutWhatIsTracked(t *testing.T) {
	steps := []store.Step{
		{ID: "a", Guild: "masonry"},
		{ID: "b", Guild: "joinery"},
		{ID: "c", Guild: "joinery"},
	}
	live := store.Dispatch{Guild: "masonry", Status: store.DispatchRunning}
	done := store.Dispatch{Guild: "joinery", Status: store.DispatchReported}

	got := StepStates(steps, []store.Dispatch{live, done})
	if got["a"] != StepRunning || got["b"] != StepNotTracked || got["c"] != StepNotTracked {
		t.Fatalf("one live Guild with a lone step: %v", got)
	}

	shared := store.Dispatch{Guild: "joinery", Status: store.DispatchQueued}
	got = StepStates(steps, []store.Dispatch{shared})
	if got["b"] != StepGuildLive || got["c"] != StepGuildLive || got["a"] != StepNotTracked {
		t.Fatalf("live Guild shared by two steps: %v", got)
	}
}
