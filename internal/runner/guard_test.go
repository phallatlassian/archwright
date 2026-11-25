package runner

import (
	"errors"
	"testing"
)

func TestTheNeverListRefusesWhatWidensPermissions(t *testing.T) {
	for name, s := range map[string]Spawn{
		"bypass mode":        {Mode: "bypassPermissions"},
		"unknown mode":       {Mode: "auto"},
		"settings override":  {Settings: "{}"},
		"mcp override":       {MCPConfig: "{}"},
		"allow rule":         {AllowRules: []string{"Bash(*)"}},
		"settings write":     {SettingsWrites: []string{"settings.json"}},
		"sandbox disabled":   {DisableSandbox: true},
		"skip permissions":   {ExtraArgs: []string{"--dangerously-skip-permissions"}},
		"sandbox flag":       {ExtraArgs: []string{"-s", "workspace-write"}},
		"config override":    {ExtraArgs: []string{"-c", "sandbox_workspace_write.writable_roots=[]"}},
		"flag with a value":  {ExtraArgs: []string{"--sandbox=danger-full-access"}},
		"value in an arg":    {ExtraArgs: []string{"--model", "x", "bypassPermissions"}},
		"short flag, joined": {ExtraArgs: []string{"-sdanger"}},
	} {
		if err := Check(s); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestTheNeverListAdmitsWhatOnlyRestricts(t *testing.T) {
	for _, s := range []Spawn{{}, {Mode: "default"}, {Mode: "plan"}, {Mode: "dontAsk"}, {ExtraArgs: []string{"--disallowed-tools", "Bash"}}} {
		if err := Check(s); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

func TestOnlyArchwrightAndTheGitConfigVariablesMayBeSet(t *testing.T) {
	if err := CheckRequest(map[string]string{"ARCHWRIGHT_RUN": "r", EnvGitConfig: "/x"}, Spawn{}); err != nil {
		t.Fatalf("allowed variables refused: %v", err)
	}
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "HOME", "ANTHROPIC_API_KEY"} {
		if err := CheckRequest(map[string]string{k: "x"}, Spawn{}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: err = %v", k, err)
		}
	}
	if err := CheckRequest(nil, Spawn{Mode: "bypassPermissions"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("the Spawn is checked too: %v", err)
	}
}

func TestEffectiveModeDefaultsToTheRunnersOwn(t *testing.T) {
	if got := (Spawn{}).EffectiveMode(); got != ModeDefault {
		t.Errorf("mode = %q", got)
	}
	if got := (Spawn{Mode: "plan"}).EffectiveMode(); got != "plan" {
		t.Errorf("mode = %q", got)
	}
}
