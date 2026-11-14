//go:build nodoctorgate

package main

import (
	"context"

	"github.com/phallatlassian/archwright/internal/paths"
)

// preflight is no gate. Only the test build uses this tag, so the tests that
// are not about doctor never start a runner on the machine they run on; a
// production binary is built without it.
func preflight(paths.Dirs) func(context.Context) error { return nil }
