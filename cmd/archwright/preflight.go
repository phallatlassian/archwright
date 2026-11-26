//go:build !nodoctorgate

package main

import (
	"context"

	"github.com/phallatlassian/archwright/internal/doctor"
	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/store"
)

// preflight is what must pass before the Works starts any run: `archwright
// doctor` (ADR 0014). It runs doctor against the real machine: its runners, tmux,
// hooks and policy files.
//
// It runs inside the Works, so it reads the registry through the Works' own
// store rather than opening the database files by path.
func preflight(dirs paths.Dirs) func(func() (*store.Snapshot, error)) func(context.Context) error {
	return func(snapshot func() (*store.Snapshot, error)) func(context.Context) error {
		gate := doctor.NewGate(func(ctx context.Context) doctor.Report {
			d := doctor.DefaultDeps(dirs)
			d.Snapshot = snapshot
			return doctor.Run(ctx, d)
		})
		return gate.Check
	}
}
