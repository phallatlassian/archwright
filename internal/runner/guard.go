package runner

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// ErrForbidden marks a spawn request the never-list refuses (ADR 0011).
var ErrForbidden = errors.New("forbidden by the never-list")

// The never-list is hard-coded and not configurable: nothing here is read
// from configuration, and no option turns an entry off.

// permittedModes are the permission modes a run may ask for: the runner's
// default and the two that only restrict it. Every other value, known or not,
// is refused, so a mode added later fails closed.
// ModeDefault is the runner's own default permission mode.
const ModeDefault = "default"

// ModeDontAsk denies what would prompt; it only restricts.
const ModeDontAsk = "dontAsk"

var permittedModes = map[string]bool{"": true, "default": true, "plan": true, "dontAsk": true}

// forbiddenFlags are runner flags refused outright, in any spelling. This
// includes -s and -c: the one bounded exception (ADR 0023) is not a pass-through,
// it is rendered by the Codex adapter from Spawn.WritableRoots, which only the
// Works sets.
var forbiddenFlags = []string{
	"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions",
	"--yolo", "--approve-for-me",
	"--dangerously-bypass-approvals-and-sandbox",
	"--permission-mode", "--permission-prompt-tool", "--permission-prompts",
	"--ask-for-approval", "-a", "--sandbox", "-s", "--full-auto",
	"--settings", "--setting-sources", "--mcp-config", "--strict-mcp-config",
	"--allowedtools", "--allowed-tools", "--allow-tools", "--tools",
	"--add-dir", "--bare",
	"-c", "--config", "--profile", "-p",
	// Codex flags that skip or replace the developer's own hook trust, rules
	// and config, or toggle features as -c does.
	"--dangerously-bypass-hook-trust", "--ignore-rules", "--ignore-user-config", "--enable", "--disable",
}

// forbiddenValues are values refused wherever they appear in an argument.
var forbiddenValues = []string{"bypasspermissions", "danger-full-access", "dangerouslydisablesandbox"}

// EnvPrefix is the only prefix a run's extra environment may carry (ADR 0018),
// besides EnvGitConfig. Anything else could point the runner at other settings or managed keys.
const EnvPrefix = "ARCHWRIGHT_"

// EnvGitConfig is the one variable outside EnvPrefix a run may carry: the
// Works' own git configuration that hides delivered skills from git (ADR 0018).
const EnvGitConfig = "GIT_CONFIG_GLOBAL"

// CheckRequest refuses a request the never-list forbids. An adapter calls it
// before spawning anything.
func CheckRequest(req StartRequest) error {
	for k := range req.Env {
		if !strings.HasPrefix(k, EnvPrefix) && k != EnvGitConfig {
			return refuse("environment variable %q may not be set; only %s* and %s are allowed", k, EnvPrefix, EnvGitConfig)
		}
	}
	return Check(req.Spawn)
}

// Check refuses a Spawn the never-list forbids.
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
	if err := checkWritableRoots(s); err != nil {
		return err
	}
	for _, a := range s.ExtraArgs {
		name := strings.ToLower(a)
		if i := strings.IndexByte(name, '='); i > 0 && strings.HasPrefix(name, "-") {
			name = name[:i]
		}
		if len(name) > 2 && name[0] == '-' && name[1] != '-' && strings.IndexByte("ascp", name[1]) >= 0 {
			name = name[:2]
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

// maxWritableRoots bounds the list; a worktree needs four.
const maxWritableRoots = 8

// checkWritableRoots validates the roots the Works computed for a Codex Wright
// (ADR 0023). They become one quoted array in a config override, so each must
// be a clean absolute path that cannot break out of its quotes or name a root
// as wide as the filesystem. The mode must not be one the adapter would
// refuse alongside a write sandbox.
func checkWritableRoots(s Spawn) error {
	if len(s.WritableRoots) == 0 {
		return nil
	}
	if len(s.WritableRoots) > maxWritableRoots {
		return refuse("%d writable roots; at most %d", len(s.WritableRoots), maxWritableRoots)
	}
	if m := s.EffectiveMode(); m != ModeDefault && m != ModeDontAsk {
		return refuse("writable roots may not be combined with permission mode %q", m)
	}
	for _, r := range s.WritableRoots {
		if !filepath.IsAbs(r) || filepath.Clean(r) != r || r == string(filepath.Separator) || strings.ContainsAny(r, "\"\\,=[]") {
			return refuse("writable root %q is not a clean absolute path", r)
		}
		for _, c := range r {
			if unicode.IsControl(c) {
				return refuse("writable root %q has a control character", r)
			}
		}
	}
	return nil
}
