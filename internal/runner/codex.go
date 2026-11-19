package runner

import (
	"context"
	"fmt"
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

// WritableRootsKey is the one `-c` key the adapter ever passes, and only with
// roots the Works computed (ADR 0023).
const WritableRootsKey = "sandbox_workspace_write.writable_roots"

// args builds the flags of one run. The brief is not among them. An approval
// policy is never passed, and a sandbox only for a Wright step the Works gave
// writable roots (`-s workspace-write` and the one roots key, ADR 0023); every
// other run inherits both, and plain `codex exec` denies what it cannot
// approve. MaxTurns and MaxBudgetUSD have no codex equivalent and are not applied.
func (c *Codex) args(req StartRequest) []string {
	flags := []string{"--json", "--ephemeral"}
	if roots := req.Spawn.WritableRoots; len(roots) > 0 {
		flags = append(flags, "-s", SandboxWorkspaceWrite, "-c", WritableRootsKey+"="+tomlStrings(roots))
	}
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
	if m := req.Spawn.EffectiveMode(); m != ModeDefault && m != ModeDontAsk {
		// codex has no permission mode flag; "default" and "dontAsk" are both
		// what `codex exec` does anyway, and nothing else can be honoured.
		return nil, fmt.Errorf("codex has no %q permission mode", m)
	}
	flags := append([]string{"exec"}, c.args(req)...)
	argv := append(append([]string(nil), flags...), "--", req.Brief) // "--": the brief is never read as a flag
	spec := procSpec{
		name: "codex", bin: c.bin(), argv: argv, version: cliVersion(ctx, c.bin()), postureFlags: flags,
		grace: c.Grace, parser: &codexParser{},
	}
	if roots := req.Spawn.WritableRoots; len(roots) > 0 {
		spec.sandbox, spec.roots = SandboxWorkspaceWrite, roots
		spec.parser = &codexParser{wantWrite: true}
	}
	return startProc(ctx, spec, req)
}

// tomlStrings renders roots as a TOML array of strings. Check has already
// refused any root with a character that needs escaping.
func tomlStrings(roots []string) string {
	q := make([]string, len(roots))
	for i, r := range roots {
		q[i] = `"` + r + `"`
	}
	return "[" + strings.Join(q, ",") + "]"
}
