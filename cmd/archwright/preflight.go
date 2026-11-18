//go:build !nodoctorgate

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/phallatlassian/archwright/internal/doctor"
	"github.com/phallatlassian/archwright/internal/paths"
)

// skipDoctorEnv opts out of the doctor gate (ADR 0014).
const skipDoctorEnv = "ARCHWRIGHT_SKIP_DOCTOR"

// preflight is what must pass before the Works starts any run: `archwright
// doctor` (ADR 0014). It runs doctor against the real machine, so it probes the
// installed runners.
//
// ARCHWRIGHT_SKIP_DOCTOR=1 in the Works' environment turns the gate off; the
// Works logs that at start. It is read when the Works starts, so a Works that
// is already running keeps the gate it started with.
func preflight(dirs paths.Dirs) func(context.Context) error {
	if os.Getenv(skipDoctorEnv) == "1" {
		fmt.Fprintf(os.Stderr, "warning: %s=1, so runs start without the doctor check\n", skipDoctorEnv)
		return nil
	}
	gate := doctor.NewGate(func(ctx context.Context) doctor.Report { return doctor.Run(ctx, doctor.DefaultDeps(dirs)) })
	return gate.Check
}
