//go:build !nodoctorgate

package main

import (
	"context"

	"github.com/phallatlassian/archwright/internal/doctor"
	"github.com/phallatlassian/archwright/internal/paths"
)

// preflight is what must pass before the Works starts any run: `archwright
// doctor` (ADR 0014). It runs doctor against the real machine, so it probes the
// installed runners.
func preflight(dirs paths.Dirs) func(context.Context) error {
	gate := doctor.NewGate(func(ctx context.Context) doctor.Report { return doctor.Run(ctx, doctor.DefaultDeps(dirs)) })
	return gate.Check
}
