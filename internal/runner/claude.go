package runner

import (
	"context"
	"strconv"
	"time"
)

// Claude runs agents on the stock `claude` CLI headlessly (ADR 0003).
type Claude struct {
	Binary string        // defaults to "claude"; tests point it at a stub
	Grace  time.Duration // how long a cancelled run gets after SIGTERM; default 5s
}

// Name implements Runner.
func (c *Claude) Name() string { return "claude" }

// Capabilities implements Runner. Resume exists in the CLI but is unused in
// v1, so it is not offered (ADR 0003).
func (c *Claude) Capabilities() Capabilities {
	return Capabilities{StructuredOutput: true, Usage: true, PermissionDenied: true}
}

func (c *Claude) bin() string {
	if c.Binary != "" {
		return c.Binary
	}
	return "claude"
}

// args builds the flags of one run. The brief is not among them; every
// flag is fixed here or comes from limits, and the permission mode is
// always passed explicitly so the run never inherits a looser one.
func (c *Claude) args(req StartRequest) (flags []string) {
	flags = []string{"--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-mode", req.Spawn.EffectiveMode()}
	if m := req.Limits.Model; m != "" {
		flags = append(flags, "--model", m)
	}
	if n := req.Limits.MaxTurns; n > 0 {
		flags = append(flags, "--max-turns", strconv.Itoa(n))
	}
	if b := req.Limits.MaxBudgetUSD; b > 0 {
		flags = append(flags, "--max-budget-usd", strconv.FormatFloat(b, 'f', -1, 64))
	}
	return append(flags, req.Spawn.ExtraArgs...)
}

// Start implements Runner. Runs are ephemeral and start with stdin closed.
func (c *Claude) Start(ctx context.Context, req StartRequest) (Run, error) {
	if err := CheckRequest(req); err != nil {
		return nil, err
	}
	flags := c.args(req)
	argv := append(append([]string{"-p"}, flags...), "--", req.Brief) // "--": the brief is never read as a flag
	return startProc(ctx, procSpec{
		name: "claude", bin: c.bin(), argv: argv, version: cliVersion(ctx, c.bin()), postureFlags: flags,
		grace: c.Grace, parser: &claudeParser{},
	}, req)
}
