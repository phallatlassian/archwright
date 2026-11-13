package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureEvents(t *testing.T, name string) ([]Event, *claudeParser) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p := &claudeParser{}
	var evs []Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		evs = append(evs, p.feed([]byte(line))...)
	}
	return evs, p
}

func kinds(evs []Event) []Kind {
	var k []Kind
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func TestNormalizeSuccessFixture(t *testing.T) {
	evs, p := fixtureEvents(t, "success.jsonl")
	want := []Kind{KindStarted, KindMessage, KindToolUse, KindToolResult, KindMessage, KindResult}
	if got := kinds(evs); strings.Join(strs(got), ",") != strings.Join(strs(want), ",") {
		t.Fatalf("kinds = %v", got)
	}
	if evs[2].Tool != "Bash" || evs[2].Command != "ls" || evs[2].ToolUseID != "toolu_1" {
		t.Errorf("tool use = %+v", evs[2])
	}
	if evs[3].Text != "a.go\nb.go" || evs[3].IsError {
		t.Errorf("tool result = %+v", evs[3])
	}
	for _, e := range evs {
		if !json.Valid(e.Raw) {
			t.Errorf("event %s lacks its raw vendor event", e.Kind)
		}
	}
	// One raw line fans out to two events, both keep it.
	if string(evs[1].Raw) != string(evs[2].Raw) {
		t.Errorf("events from one line differ in raw")
	}
	r := p.result
	if r == nil || r.Status != StatusDone || r.Final != "Two files." || string(r.Structured) != `{"files":2}` ||
		r.Usage != (Usage{InputTokens: 120, OutputTokens: 34, CostUSD: 0.0123}) {
		t.Errorf("result = %+v", r)
	}
}

func TestNormalizeDeniedFixture(t *testing.T) {
	evs, p := fixtureEvents(t, "denied.jsonl")
	var denied []Event
	for _, e := range evs {
		if e.Kind == KindPermissionDenied {
			denied = append(denied, e)
		}
	}
	// toolu_9 arrives as a system event and again in the result; it counts once.
	if len(denied) != 2 || denied[0].Command != "rm -rf build && make dist" || denied[0].Tool != "Bash" {
		t.Fatalf("denied = %+v", denied)
	}
	if denied[1].Tool != "Edit" || !strings.Contains(denied[1].Command, `"file_path":"/work/x.go"`) {
		t.Errorf("second denial = %+v", denied[1])
	}
	if len(p.result.Denials) != 2 {
		t.Errorf("result denials = %+v", p.result.Denials)
	}
	if evs[len(evs)-1].Kind != KindResult {
		t.Errorf("result is not last: %v", kinds(evs))
	}
}

func TestNormalizeFailedFixture(t *testing.T) {
	_, p := fixtureEvents(t, "failed.jsonl")
	if p.result.Status != StatusFailed || p.result.Error != "error_max_turns" {
		t.Errorf("result = %+v", p.result)
	}
}

func TestNormalizeKeepsUnknownLines(t *testing.T) {
	p := &claudeParser{}
	for _, line := range []string{`{"type":"rate_limit_event","x":1}`, `not json`} {
		evs := p.feed([]byte(line))
		if len(evs) != 1 || evs[0].Kind != KindOther || len(evs[0].Raw) == 0 || !json.Valid(evs[0].Raw) {
			t.Errorf("line %q -> %+v", line, evs)
		}
	}
}

func strs(k []Kind) []string {
	var s []string
	for _, x := range k {
		s = append(s, string(x))
	}
	return s
}

func stubRun(t *testing.T, fixture string, env map[string]string) (Run, string) {
	t.Helper()
	dir := t.TempDir()
	stub, _ := filepath.Abs("testdata/stub-claude.sh")
	e := map[string]string{"STUB_FIXTURE": fixture, "STUB_ARGS": filepath.Join(dir, "args"), "STUB_STDIN": filepath.Join(dir, "stdin"), "STUB_PWD": filepath.Join(dir, "pwd")}
	for k, v := range env {
		e[k] = v
	}
	c := &Claude{Binary: stub, Grace: time.Second}
	run, err := c.Start(context.Background(), StartRequest{Brief: "the brief", Dir: dir, Env: e, Limits: Limits{Model: "m1", MaxTurns: 3, MaxBudgetUSD: 1.5}})
	if err != nil {
		t.Fatal(err)
	}
	return run, dir
}

func TestClaudeSpawnIsEphemeralAndStdinClosed(t *testing.T) {
	run, dir := stubRun(t, "success.jsonl", nil)
	collect(t, run)
	run.Result()
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	got := strings.Fields(string(args))
	if got[0] != "-p" || !strings.Contains(string(args), "the brief") {
		t.Errorf("args = %q", args)
	}
	for _, want := range []string{"--no-session-persistence", "--output-format", "stream-json", "--permission-mode", "default", "--max-turns", "3", "--max-budget-usd", "1.5", "--model", "m1"} {
		if !strings.Contains(" "+strings.Join(got, " ")+" ", " "+want+" ") {
			t.Errorf("missing %q in %q", want, args)
		}
	}
	if n, _ := os.ReadFile(filepath.Join(dir, "stdin")); strings.TrimSpace(string(n)) != "0" {
		t.Errorf("stdin carried %q bytes", n)
	}
	if pwd, _ := os.ReadFile(filepath.Join(dir, "pwd")); !strings.HasSuffix(strings.TrimSpace(string(pwd)), filepath.Base(dir)) {
		t.Errorf("ran in %q, want %q", pwd, dir)
	}
}

func TestClaudePostureRecordsFlagsAndVersion(t *testing.T) {
	run, _ := stubRun(t, "success.jsonl", nil)
	evs := collect(t, run)
	p := evs[0].Posture
	if p.Version != "2.1.285 (Claude Code)" || p.Mode != "default" {
		t.Errorf("posture = %+v", p)
	}
	flags := strings.Join(p.Flags, " ")
	if !strings.Contains(flags, "--no-session-persistence") || strings.Contains(flags, "the brief") {
		t.Errorf("flags = %q", flags)
	}
}

func TestClaudeKillsASessionWiderThanRequested(t *testing.T) {
	run, _ := stubRun(t, "widened.jsonl", nil)
	collect(t, run)
	res := run.Result()
	if res.Status != StatusFailed || !strings.Contains(res.Error, "bypassPermissions") {
		t.Errorf("result = %+v", res)
	}
}

func TestClaudeFailsVisiblyWithoutAResult(t *testing.T) {
	c := &Claude{Binary: "/usr/bin/false"}
	run, err := c.Start(context.Background(), StartRequest{Brief: "x", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run)
	res := run.Result()
	if res.Status != StatusFailed || !strings.Contains(res.Error, "without a result") {
		t.Errorf("result = %+v", res)
	}
}

func TestClaudeFailedFixtureResult(t *testing.T) {
	run, _ := stubRun(t, "failed.jsonl", nil)
	collect(t, run)
	if res := run.Result(); res.Status != StatusFailed || res.Usage.CostUSD != 0.5 {
		t.Errorf("result = %+v", res)
	}
}
