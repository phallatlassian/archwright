//go:build !nodoctorgate

package main

import (
	"testing"

	"github.com/phallatlassian/archwright/internal/paths"
)

func TestPreflightIsSkippedOnlyByTheOptOut(t *testing.T) {
	t.Setenv(skipDoctorEnv, "")
	if preflight(paths.Dirs{}) == nil {
		t.Error("the doctor gate is off without the opt-out")
	}
	t.Setenv(skipDoctorEnv, "0")
	if preflight(paths.Dirs{}) == nil {
		t.Error("ARCHWRIGHT_SKIP_DOCTOR=0 turned the gate off")
	}
	t.Setenv(skipDoctorEnv, "1")
	if preflight(paths.Dirs{}) != nil {
		t.Error("ARCHWRIGHT_SKIP_DOCTOR=1 did not turn the gate off")
	}
}
