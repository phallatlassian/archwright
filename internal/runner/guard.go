package runner

import (
	"errors"
	"fmt"
	"strings"
)

// ErrForbidden marks a spawn request the never-list refuses (ADR 0011).
var ErrForbidden = errors.New("forbidden by the never-list")

// The never-list is hard-coded and not configurable: nothing here is read
// from configuration, and no option turns an entry off.

// permittedModes are the permission modes a run may ask for: the runner's
// default and the two that only restrict it. Every other value, known or not,
// is refused, so a mode added later fails closed.
var permittedModes = map[string]bool{"": true, "default": true, "plan": true, "dontAsk": true}

// forbiddenFlags are runner flags refused outright, in any spelling.
var forbiddenFlags = []string{
	"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions",
	"--yolo", "--approve-for-me",
	"--dangerously-bypass-approvals-and-sandbox",
	"--permission-mode", "--permission-prompt-tool", "--permission-prompts",
	"--ask-for-approval", "-a", "--sandbox", "-s", "--full-auto",
	"--settings", "--setting-sources", "--mcp-config", "--strict-mcp-config",
	"--allowedtools", "--allowed-tools", "--allow-tools", "--tools",
	"--add-dir", "--bare",
	"-c", "--config", "--profile",
}

// forbiddenValues are values refused wherever they appear in an argument.
var forbiddenValues = []string{"bypasspermissions", "danger-full-access", "dangerouslydisablesandbox"}

// Check refuses a request the never-list forbids. An adapter calls it before
// spawning anything.
func Check(s Spawn) error {
	if !permittedModes[s.Mode] {
		return refuse("permission mode %q is more permissive than the runner's default or unknown", s.Mode)
	}
	if s.Settings != "" {
		return refuse("--settings may override managed keys")
	}
	if s.MCPConfig != "" {
		return refuse("--mcp-config may override managed keys")
	}
	if len(s.AllowRules) > 0 {
		return refuse("allow rules may not be added: %s", strings.Join(s.AllowRules, ", "))
	}
	if len(s.SettingsWrites) > 0 {
		return refuse("settings files may not be written: %s", strings.Join(s.SettingsWrites, ", "))
	}
	if s.DisableSandbox {
		return refuse("the sandbox may not be disabled")
	}
	for _, a := range s.ExtraArgs {
		name := strings.ToLower(a)
		if i := strings.IndexByte(name, '='); i > 0 && strings.HasPrefix(name, "-") {
			name = name[:i]
		}
		for _, f := range forbiddenFlags {
			if name == f {
				return refuse("flag %q is on the never-list", a)
			}
		}
		for _, v := range forbiddenValues {
			if strings.Contains(strings.ToLower(a), v) {
				return refuse("argument %q is on the never-list", a)
			}
		}
	}
	return nil
}

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrForbidden, fmt.Sprintf(format, args...))
}
