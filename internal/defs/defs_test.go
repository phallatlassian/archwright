package defs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const guildMD = `---
intake: A thing to build
handback: A built thing
runner: claude
runners: [claude, codex]
access: read-write
rework_rounds: 3
denied_tools: [WebFetch]
---
Be careful.
`

func guildFS(name, manifest string) fstest.MapFS {
	return fstest.MapFS{
		"guilds/" + name + "/GUILD.md":        {Data: []byte(manifest)},
		"guilds/" + name + "/warden/SKILL.md": {Data: []byte("Warden.")},
		"guilds/" + name + "/wrights/code.md": {Data: []byte("Wright.")},
		"jigs/go.yaml":                        {Data: []byte(jigYAML("go", name))},
	}
}

func jigYAML(name, guild string) string {
	return "name: " + name + `
description: Do the thing
parameters:
  - name: what
    required: true
steps:
  - id: one
    guild: ` + guild + `
    task: "Do {{what}}"
  - id: two
    guild: ` + guild + `
    task: Check it
    blocked_by: [one]
    checkpoint: true
`
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func errFor(r *Registry, kind, name string) *LoadError {
	for _, e := range r.Errors() {
		if e.Kind == kind && e.Name == name {
			return &e
		}
	}
	return nil
}

func TestShippedDefinitionsLoad(t *testing.T) {
	r := New(Shipped(), "")
	if errs := r.Reload(); len(errs) != 0 {
		t.Fatalf("shipped definitions invalid: %v", errs)
	}
	for _, g := range []string{"chancery", "masonry", "survey", "hallmark"} {
		if _, ok := r.Guild(g); !ok {
			t.Errorf("guild %q not shipped", g)
		}
	}
	for _, j := range []string{"fix", "build", "specify", "map", "investigate", "review"} {
		if _, ok := r.Jig(j); !ok {
			t.Errorf("jig %q not shipped", j)
		}
	}
	rg, _ := r.Guild("survey")
	if rg.Access != ReadOnly || rg.Wrights["docs"] == "" || rg.Wrights["code-trace"] == "" {
		t.Errorf("survey guild: %+v", rg)
	}
}

func TestOverrideByNameReplacesEntirely(t *testing.T) {
	cfg := t.TempDir()
	r := New(guildFS("eng", guildMD), cfg)
	r.Reload()
	write(t, cfg, "guilds/eng/GUILD.md", "---\nintake: mine\nhandback: mine\nrunner: codex\nrunners: [codex]\n---\nMy charter.\n")
	write(t, cfg, "guilds/eng/warden/SKILL.md", "My warden.")
	if errs := r.Reload(); len(errs) != 0 {
		t.Fatal(errs)
	}
	g, _ := r.Guild("eng")
	if g.Intake != "mine" || g.Charter != "My charter." || g.ReworkRounds != 0 || len(g.DeniedTools) != 0 || len(g.Wrights) != 0 {
		t.Errorf("override merged with shipped: %+v", g)
	}
	write(t, cfg, "jigs/go.yaml", "name: go\ndescription: mine\nsteps:\n  - id: only\n    guild: eng\n    task: x\n")
	r.Reload()
	j, _ := r.Jig("go")
	if j.Description != "mine" || len(j.Steps) != 1 || len(j.Parameters) != 0 {
		t.Errorf("jig override merged: %+v", j)
	}
}

func TestInvalidGuildKeepsPreviousAndReportsError(t *testing.T) {
	cfg := t.TempDir()
	r := New(guildFS("eng", guildMD), cfg)
	write(t, cfg, "guilds/eng/GUILD.md", strings.Replace(guildMD, "A thing to build", "good override", 1))
	write(t, cfg, "guilds/eng/warden/SKILL.md", "w")
	r.Reload()
	if g, _ := r.Guild("eng"); g.Intake != "good override" {
		t.Fatalf("setup: %+v", g)
	}
	write(t, cfg, "guilds/eng/GUILD.md", "---\nintake: [broken\n---\n")
	errs := r.Reload()
	if len(errs) != 1 || errFor(r, "guild", "eng") == nil {
		t.Fatalf("errors: %v", errs)
	}
	if g, _ := r.Guild("eng"); g.Intake != "good override" {
		t.Errorf("previous good definition lost: %+v", g)
	}
	// With no previous override, the shipped one stays.
	cfg2 := t.TempDir()
	r2 := New(guildFS("eng", guildMD), cfg2)
	r2.Reload()
	write(t, cfg2, "guilds/eng/GUILD.md", "not a manifest")
	if errs := r2.Reload(); len(errs) != 1 {
		t.Fatalf("errors: %v", errs)
	}
	if g, ok := r2.Guild("eng"); !ok || g.Intake != "A thing to build" {
		t.Errorf("shipped definition lost: %+v", g)
	}
	// A broken config-only guild is simply absent.
	write(t, cfg2, "guilds/new/GUILD.md", "---\nbogus: 1\n---\nx")
	r2.Reload()
	if _, ok := r2.Guild("new"); ok {
		t.Error("invalid guild loaded")
	}
}

func TestInvalidGuildRules(t *testing.T) {
	cases := map[string]string{
		"widening key":     "---\nintake: a\nhandback: b\nrunner: claude\nrunners: [claude]\npermission_mode: bypassPermissions\n---\nx",
		"allowed tools":    "---\nintake: a\nhandback: b\nrunner: claude\nrunners: [claude]\nallowed_tools: [Bash]\n---\nx",
		"unknown runner":   "---\nintake: a\nhandback: b\nrunner: gpt\nrunners: [gpt]\n---\nx",
		"default not in":   "---\nintake: a\nhandback: b\nrunner: claude\nrunners: [codex]\n---\nx",
		"bad access":       "---\nintake: a\nhandback: b\nrunner: claude\nrunners: [claude]\naccess: root\n---\nx",
		"unknown requires": "---\nintake: a\nhandback: b\nrunner: claude\nrunners: [claude]\nrequires: [magic]\n---\nx",
		"no charter":       "---\nintake: a\nhandback: b\nrunner: claude\nrunners: [claude]\n---\n",
		"no intake":        "---\nhandback: b\nrunner: claude\nrunners: [claude]\n---\nx",
		"negative rework":  "---\nintake: a\nhandback: b\nrunner: claude\nrunners: [claude]\nrework_rounds: -1\n---\nx",
	}
	for name, manifest := range cases {
		cfg := t.TempDir()
		write(t, cfg, "guilds/g/GUILD.md", manifest)
		write(t, cfg, "guilds/g/warden/SKILL.md", "w")
		r := New(fstest.MapFS{}, cfg)
		r.Reload()
		if _, ok := r.Guild("g"); ok || errFor(r, "guild", "g") == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	cfg := t.TempDir()
	write(t, cfg, "guilds/g/GUILD.md", guildMD) // no warden skill
	r := New(fstest.MapFS{}, cfg)
	r.Reload()
	if _, ok := r.Guild("g"); ok {
		t.Error("guild without warden/SKILL.md accepted")
	}
}

func TestJigNamingUnknownGuildRejectedAtLoad(t *testing.T) {
	cfg := t.TempDir()
	r := New(guildFS("eng", guildMD), cfg)
	write(t, cfg, "jigs/bad.yaml", jigYAML("bad", "nowhere"))
	r.Reload()
	if _, ok := r.Jig("bad"); ok {
		t.Fatal("jig naming an unknown guild loaded")
	}
	e := errFor(r, "jig", "bad")
	if e == nil || !strings.Contains(e.Err.Error(), "nowhere") {
		t.Fatalf("error: %v", e)
	}
	// A jig can name a guild the developer added.
	write(t, cfg, "guilds/nowhere/GUILD.md", guildMD)
	write(t, cfg, "guilds/nowhere/warden/SKILL.md", "w")
	if errs := r.Reload(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, ok := r.Jig("bad"); !ok {
		t.Error("jig not loaded once its guild exists")
	}
}

func TestInvalidJigKeepsPrevious(t *testing.T) {
	cfg := t.TempDir()
	r := New(guildFS("eng", guildMD), cfg)
	r.Reload()
	write(t, cfg, "jigs/go.yaml", "name: go\ndescription: v2\nsteps:\n  - id: a\n    guild: eng\n    task: x\n")
	r.Reload()
	for name, body := range map[string]string{
		"cycle":    "name: go\ndescription: d\nsteps:\n  - {id: a, guild: eng, task: x, blocked_by: [b]}\n  - {id: b, guild: eng, task: x, blocked_by: [a]}\n",
		"unknown":  "name: go\ndescription: d\nsteps:\n  - {id: a, guild: eng, task: x, bogus: 1}\n",
		"no steps": "name: go\ndescription: d\nsteps: []\n",
		"param":    "name: go\ndescription: d\nsteps:\n  - {id: a, guild: eng, task: \"{{nope}}\"}\n",
		"name":     "name: other\ndescription: d\nsteps:\n  - {id: a, guild: eng, task: x}\n",
		"no desc":  "name: go\nsteps:\n  - {id: a, guild: eng, task: x}\n",
		"yaml":     "name: [",
		"blocked":  "name: go\ndescription: d\nsteps:\n  - {id: a, guild: eng, task: x, blocked_by: [zz]}\n",
	} {
		write(t, cfg, "jigs/go.yaml", body)
		r.Reload()
		if j, _ := r.Jig("go"); j.Description != "v2" {
			t.Errorf("%s: previous jig lost: %+v", name, j)
		}
		if errFor(r, "jig", "go") == nil {
			t.Errorf("%s: no error surfaced", name)
		}
	}
	// Errors clear once the file is fixed.
	write(t, cfg, "jigs/go.yaml", "name: go\ndescription: v3\nsteps:\n  - {id: a, guild: eng, task: x}\n")
	if errs := r.Reload(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if j, _ := r.Jig("go"); j.Description != "v3" {
		t.Errorf("fixed jig not loaded: %+v", j)
	}
}

func TestInstantiate(t *testing.T) {
	r := New(guildFS("eng", guildMD), "")
	r.Reload()
	j, _ := r.Jig("go")
	steps, err := j.Instantiate(map[string]string{"what": "the thing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].Brief != "Do the thing" || steps[0].Title != "Do the thing" || !steps[1].Checkpoint || steps[1].BlockedBy[0] != "one" || steps[0].Guild != "eng" {
		t.Errorf("steps: %+v", steps)
	}
	if _, err := j.Instantiate(nil); err == nil {
		t.Error("missing required parameter accepted")
	}
	if _, err := j.Instantiate(map[string]string{"what": "x", "extra": "y"}); err == nil {
		t.Error("unknown parameter accepted")
	}
}

func TestBindOnlyNarrows(t *testing.T) {
	g := Guild{Name: "g", Access: ReadWrite, Runner: "claude", Runners: []string{"claude", "codex"}, DeniedTools: []string{"WebFetch"}}
	all := []string{CapReadOnly, CapToolDenylist, CapIsolatedWorktree}
	profiles := map[string]Profile{
		"claude": {Capabilities: all, Access: ReadOnly, DeniedTools: []string{"Bash(rm:*)"}},
		"codex":  {Capabilities: all},
	}
	b, err := Bind(g, "", profiles)
	if err != nil {
		t.Fatal(err)
	}
	if b.Access != ReadOnly {
		t.Errorf("guild read-write widened a read-only developer config: %s", b.Access)
	}
	if strings.Join(b.DeniedTools, ",") != "Bash(rm:*),WebFetch" {
		t.Errorf("denied tools not unioned: %v", b.DeniedTools)
	}
	if !b.DetachedWorktree {
		t.Error("read-only binding must use a detached worktree")
	}
	b, err = Bind(g, "codex", profiles)
	if err != nil || b.Access != ReadWrite || b.Runner != "codex" || b.DetachedWorktree {
		t.Errorf("codex binding: %+v %v", b, err)
	}
	g.Access = ReadOnly
	b, _ = Bind(g, "codex", profiles)
	if b.Access != ReadOnly {
		t.Error("guild's own read-only dropped")
	}
	if _, err := Bind(g, "gemini", profiles); err == nil {
		t.Error("runner outside the permitted set accepted")
	}
	g.Runners = []string{"claude"}
	if _, err := Bind(g, "codex", profiles); err == nil {
		t.Error("runner outside the guild's permitted set accepted")
	}
}

func TestBindFailsOnUnmetCapability(t *testing.T) {
	g := Guild{Name: "g", Access: ReadOnly, Runner: "claude", Runners: []string{"claude"}}
	_, err := Bind(g, "", map[string]Profile{"claude": {Capabilities: []string{CapToolDenylist}}})
	if !errors.Is(err, ErrCapability) || !strings.Contains(err.Error(), CapReadOnly) {
		t.Errorf("read-only not enforceable: %v", err)
	}
	g = Guild{Name: "g", Access: ReadWrite, Runner: "claude", Runners: []string{"claude"}, DeniedTools: []string{"x"}}
	if _, err := Bind(g, "", map[string]Profile{"claude": {}}); !errors.Is(err, ErrCapability) {
		t.Errorf("denied tools not enforceable: %v", err)
	}
	g = Guild{Name: "g", Access: ReadWrite, Runner: "claude", Runners: []string{"claude"}, Requires: []string{CapIsolatedWorktree}}
	if _, err := Bind(g, "", map[string]Profile{"claude": {}}); !errors.Is(err, ErrCapability) {
		t.Errorf("required capability: %v", err)
	}
	if _, err := Bind(g, "", map[string]Profile{}); !errors.Is(err, ErrCapability) {
		t.Errorf("unknown runner profile: %v", err)
	}
}
