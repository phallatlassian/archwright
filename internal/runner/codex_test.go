package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func codexFixtureEvents(t *testing.T, name string) ([]Event, *codexParser) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p := &codexParser{}
	var evs []Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		evs = append(evs, p.feed([]byte(line))...)
	}
	return evs, p
}

func TestNormalizeCodexSuccessFixture(t *testing.T) {
	evs, p := codexFixtureEvents(t, "codex-success.jsonl")
	want := []Kind{KindStarted, KindOther, KindOther, KindOther, KindToolUse, KindToolResult, KindToolUse, KindToolResult, KindMessage, KindResult}
	if got := kinds(evs); strings.Join(strs(got), ",") != strings.Join(strs(want), ",") {
		t.Fatalf("kinds = %v", got)
	}
	// turn.started, the managed-policy notice (an error item), reasoning
	if !strings.Contains(evs[2].Text, "falling back to required value OnRequest") {
		t.Errorf("notice = %+v", evs[2])
	}
	if e := evs[4]; e.Tool != "shell" || e.Command != "/bin/zsh -lc ls" || e.ToolUseID != "item_2" {
		t.Errorf("tool use = %+v", e)
	}
	if e := evs[5]; e.Text != "a.go\nb.go\n" || e.IsError || e.ToolUseID != "item_2" {
		t.Errorf("tool result = %+v", e)
	}
	if e := evs[6]; e.Tool != "file_change" || !strings.Contains(e.Command, "/work/a.go") {
		t.Errorf("file change = %+v", e)
	}
	for _, e := range evs {
		if !json.Valid(e.Raw) {
			t.Errorf("event %s lacks its raw vendor event", e.Kind)
		}
	}
	r := p.result
	if r == nil || r.Status != StatusDone || r.Final != "Two files." || r.Usage != (Usage{InputTokens: 1200, OutputTokens: 34}) {
		t.Errorf("result = %+v", r)
	}
}

func TestNormalizeCodexDeniedFixture(t *testing.T) {
	evs, p := codexFixtureEvents(t, "codex-denied.jsonl")
	var denied []Event
	for _, e := range evs {
		if e.Kind == KindPermissionDenied {
			denied = append(denied, e)
		}
	}
	// Only the escalation that could not be approved (exit -1) is a denial;
	// an ordinary failing command (exit 1) is a failed tool result.
	if len(denied) != 1 || denied[0].Command != "/bin/zsh -lc 'curl https://example.com'" || denied[0].Tool != "shell" || denied[0].ToolUseID != "item_0" {
		t.Fatalf("denied = %+v", denied)
	}
	var failed int
	for _, e := range evs {
		if e.Kind == KindToolResult && e.IsError {
			failed++
		}
	}
	if failed != 2 {
		t.Errorf("%d failed tool results", failed)
	}
	if len(p.result.Denials) != 1 || p.result.Status != StatusDone {
		t.Errorf("result = %+v", p.result)
	}
	if evs[len(evs)-1].Kind != KindResult {
		t.Errorf("result is not last: %v", kinds(evs))
	}
}

func TestNormalizeCodexFailedFixture(t *testing.T) {
	evs, p := codexFixtureEvents(t, "codex-failed.jsonl")
	if p.result == nil || p.result.Status != StatusFailed || p.result.Error != "stream disconnected before completion" {
		t.Errorf("result = %+v", p.result)
	}
	if last := evs[len(evs)-1]; last.Kind != KindResult || !last.IsError {
		t.Errorf("last = %+v", last)
	}
}

func TestNormalizeCodexKeepsUnknownLines(t *testing.T) {
	p := &codexParser{}
	for _, line := range []string{`{"type":"future.event","x":1}`, `not json`, `{"type":"item.completed","item":{"id":"i","type":"web_search"}}`} {
		evs := p.feed([]byte(line))
		if len(evs) != 1 || evs[0].Kind != KindOther || !json.Valid(evs[0].Raw) {
			t.Errorf("line %q -> %+v", line, evs)
		}
	}
}

func TestNormalizeCodexCompletedCommandWithoutStart(t *testing.T) {
	p := &codexParser{}
	evs := p.feed([]byte(`{"type":"item.completed","item":{"id":"i","type":"command_execution","command":"ls","aggregated_output":"x","exit_code":0,"status":"completed"}}`))
	if got := kinds(evs); len(got) != 2 || got[0] != KindToolUse || got[1] != KindToolResult {
		t.Errorf("kinds = %v", got)
	}
}

func codexStubRun(t *testing.T, fixture string, req StartRequest) (Run, string, error) {
	t.Helper()
	dir := t.TempDir()
	stub, _ := filepath.Abs("testdata/stub-codex.sh")
	req.Dir = dir
	req.Env = map[string]string{"ARCHWRIGHT_STUB_FIXTURE": fixture, "ARCHWRIGHT_STUB_ARGS": filepath.Join(dir, "args"), "ARCHWRIGHT_STUB_STDIN": filepath.Join(dir, "stdin"), "ARCHWRIGHT_STUB_PWD": filepath.Join(dir, "pwd")}
	run, err := (&Codex{Binary: stub, Grace: time.Second}).Start(context.Background(), req)
	return run, dir, err
}

func TestCodexSpawnIsEphemeralAndStdinClosed(t *testing.T) {
	run, dir, err := codexStubRun(t, "codex-success.jsonl", StartRequest{Brief: "the brief", Limits: Limits{Model: "m1", MaxTurns: 3}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run)
	run.Result()
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	got := strings.Fields(string(args))
	if got[0] != "exec" || !strings.HasSuffix(string(args), "--\nthe brief\n") {
		t.Errorf("args = %q", args)
	}
	for _, want := range []string{"--json", "--ephemeral", "--model", "m1"} {
		if !strings.Contains(" "+strings.Join(got, " ")+" ", " "+want+" ") {
			t.Errorf("missing %q in %q", want, args)
		}
	}
	// No sandbox, approval or bypass flag is ever passed on the adapter's own account.
	for _, bad := range []string{"--sandbox", "-s", "--ask-for-approval", "-c", "--approve-for-me", "--yolo", "--full-auto", "--dangerously-bypass-approvals-and-sandbox", "--max-turns"} {
		for _, g := range got {
			if g == bad {
				t.Errorf("unexpected flag %q in %q", bad, args)
			}
		}
	}
	if n, _ := os.ReadFile(filepath.Join(dir, "stdin")); strings.TrimSpace(string(n)) != "0" {
		t.Errorf("stdin carried %q bytes", n)
	}
	if pwd, _ := os.ReadFile(filepath.Join(dir, "pwd")); !strings.HasSuffix(strings.TrimSpace(string(pwd)), filepath.Base(dir)) {
		t.Errorf("ran in %q, want %q", pwd, dir)
	}
}

func TestCodexPostureRecordsFlagsAndVersion(t *testing.T) {
	run, _, err := codexStubRun(t, "codex-success.jsonl", StartRequest{Brief: "the brief"})
	if err != nil {
		t.Fatal(err)
	}
	p := collect(t, run)[0].Posture
	if p.Runner != "codex" || p.Version != "codex-cli 0.162.1" || p.Mode != "default" || p.Sandbox != "inherited" {
		t.Errorf("posture = %+v", p)
	}
	flags := strings.Join(p.Flags, " ")
	if !strings.Contains(flags, "--ephemeral") || !strings.Contains(flags, "--json") || strings.Contains(flags, "the brief") {
		t.Errorf("flags = %q", flags)
	}
}

func TestCodexRefusesWhatClaudeRefuses(t *testing.T) {
	for name, sp := range forbiddenSpawns {
		t.Run(name, func(t *testing.T) {
			run, _, err := codexStubRun(t, "codex-success.jsonl", StartRequest{Brief: "x", Spawn: sp})
			if !errors.Is(err, ErrForbidden) || run != nil {
				t.Fatalf("run = %v, err = %v", run, err)
			}
		})
	}
	// Flags only codex has.
	for _, f := range []string{"--dangerously-bypass-hook-trust", "--ignore-rules", "--ignore-user-config", "--enable", "--disable", "-c", "--config=approval_policy=never", "--profile", "-p", "-sworkspace-write", "-aon-request", "-cfoo=bar", "-pdev", "--add-dir"} {
		if err := Check(Spawn{ExtraArgs: []string{f}}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%q: %v", f, err)
		}
	}
}

func TestCodexRefusesUnsupportedMode(t *testing.T) {
	run, _, err := codexStubRun(t, "codex-success.jsonl", StartRequest{Brief: "x", Spawn: Spawn{Mode: "plan"}})
	if err == nil || run != nil || errors.Is(err, ErrForbidden) {
		t.Fatalf("run = %v, err = %v", run, err)
	}
}

func TestCodexFailedFixtureResult(t *testing.T) {
	run, _, err := codexStubRun(t, "codex-failed.jsonl", StartRequest{Brief: "x"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run)
	if res := run.Result(); res.Status != StatusFailed || !strings.Contains(res.Error, "stream disconnected") {
		t.Errorf("result = %+v", res)
	}
}

func TestCodexFailsVisiblyWithoutAResult(t *testing.T) {
	run, err := (&Codex{Binary: "/usr/bin/false"}).Start(context.Background(), StartRequest{Brief: "x", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run)
	res := run.Result()
	if res.Status != StatusFailed || !strings.Contains(res.Error, "codex ended without a result") {
		t.Errorf("result = %+v", res)
	}
}

func TestCodexPostureRecordsManagedPolicyNotices(t *testing.T) {
	run, _, err := codexStubRun(t, "codex-success.jsonl", StartRequest{Brief: "x"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, run)
	if evs[0].Kind != KindPosture || len(evs[0].Posture.Notices) != 1 || !strings.Contains(evs[0].Posture.Notices[0], "falling back to required value OnRequest") {
		t.Fatalf("posture = %+v", evs[0].Posture)
	}
	for i, e := range evs {
		if e.Seq != i+1 {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
	if evs[1].Kind != KindStarted {
		t.Errorf("held events out of order: %v", kinds(evs))
	}
}
