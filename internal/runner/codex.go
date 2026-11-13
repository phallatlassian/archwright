package runner

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Codex runs agents on the stock `codex` CLI headlessly (ADR 0003).
type Codex struct {
	Binary string        // defaults to "codex"; tests point it at a stub
	Grace  time.Duration // how long a cancelled run gets after SIGTERM; default 5s
}

// Name implements Runner.
func (c *Codex) Name() string { return "codex" }

// Capabilities implements Runner. Codex reports token usage but no cost, and
// structured output (--output-schema) is not offered by the contract.
func (c *Codex) Capabilities() Capabilities {
	return Capabilities{Usage: true, PermissionDenied: true}
}

func (c *Codex) bin() string {
	if c.Binary != "" {
		return c.Binary
	}
	return "codex"
}

// args builds the flags of one run. The brief is not among them. Neither a
// sandbox nor an approval policy is ever passed: both are inherited, and
// plain `codex exec` denies what it cannot approve. MaxTurns and MaxBudgetUSD
// have no codex equivalent and are not applied.
func (c *Codex) args(req StartRequest) []string {
	flags := []string{"--json", "--ephemeral"}
	if m := req.Limits.Model; m != "" {
		flags = append(flags, "--model", m)
	}
	return append(flags, req.Spawn.ExtraArgs...)
}

// Start implements Runner. Runs are ephemeral and start with stdin closed
// (codex otherwise waits for it to end).
func (c *Codex) Start(ctx context.Context, req StartRequest) (Run, error) {
	if err := CheckRequest(req); err != nil {
		return nil, err
	}
	if m := req.Spawn.EffectiveMode(); m != ModeDefault && m != "dontAsk" {
		// codex has no permission mode flag; "default" and "dontAsk" are both
		// what `codex exec` does anyway, and nothing else can be honoured.
		return nil, fmt.Errorf("codex has no %q permission mode", m)
	}
	flags := c.args(req)
	argv := append(append([]string{"exec"}, flags...), "--", req.Brief) // "--": the brief is never read as a flag
	return startProc(ctx, procSpec{
		name: "codex", bin: c.bin(), argv: argv, version: c.version(ctx), flags: append([]string{"exec"}, flags...),
		grace: c.Grace, parser: &codexParser{},
	}, req)
}

// version asks the CLI for its version; a failure is recorded as "unknown"
// rather than stopping the run.
func (c *Codex) version(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.bin(), "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
