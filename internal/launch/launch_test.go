package launch

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/paths"
	"github.com/phallatlassian/archwright/internal/runner"
	"github.com/phallatlassian/archwright/internal/spool"
)

var run = spool.Env{Run: "run-1", Dir: "/tmp/s", Token: "tok"}

func TestReadRunnerDefaultsToClaudeAndReadsConfig(t *testing.T) {
	d := paths.Dirs{Config: t.TempDir()}
	if got, err := ReadRunner(d); err != nil || got != RunnerClaude {
		t.Fatalf("absent config = %q, %v", got, err)
	}
	os.WriteFile(ConfigFile(d), []byte("# install\nrunner = \"codex\" # chosen\n"), 0o600)
	if got, err := ReadRunner(d); err != nil || got != RunnerCodex {
		t.Fatalf("got %q, %v", got, err)
	}
	os.WriteFile(ConfigFile(d), []byte("runner = \"grok\"\n"), 0o600)
	if _, err := ReadRunner(d); err == nil {
		t.Fatal("an unknown runner must be refused")
	}
}

func TestBuildClaudeSuppliesOnlyHooksInSettings(t *testing.T) {
	cmd, err := Build(Input{Runner: RunnerClaude, Exe: "/opt/it's/archwright", Home: "/home/neutral", Site: "shop", Run: run})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != "/home/neutral" || !cmd.Hooks {
		t.Fatalf("dir/hooks: %+v", cmd)
	}
	var settings map[string]map[string][]struct {
		Hooks []struct{ Type, Command string }
	}
	i := indexOf(cmd.Args, "--settings")
	raw := cmd.Args[i+1]
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil || len(top) != 1 || top["hooks"] == nil {
		t.Fatalf("settings must carry only hooks: %s", raw)
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		if got := settings["hooks"][ev][0].Hooks[0].Command; !strings.Contains(got, `'/opt/it'\''s/archwright' hook `) {
			t.Fatalf("%s command %q", ev, got)
		}
	}
	// Everything else passed is within the never-list.
	var rest []string
	for j, a := range cmd.Args {
		if j != i && j != i+1 {
			rest = append(rest, a)
		}
	}
	if err := runner.Check(runner.Spawn{ExtraArgs: rest}); err != nil {
		t.Fatal(err)
	}
	if !contains(cmd.Env, "ARCHWRIGHT_SITE=shop") || !contains(cmd.Env, "ARCHWRIGHT_RUN_TOKEN=tok") {
		t.Fatalf("env: %v", cmd.Env)
	}
}

func TestBuildCodexPollsAndHasNoSettings(t *testing.T) {
	cmd, err := Build(Input{Runner: RunnerCodex, Exe: "/x", Home: "/h", Run: run})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Hooks || indexOf(cmd.Args, "--settings") >= 0 || indexOf(cmd.Args, "-c") >= 0 {
		t.Fatalf("codex gets no settings: %v", cmd.Args)
	}
	if got := cmd.Args[len(cmd.Args)-1]; !strings.Contains(got, "read_inbox") || !strings.Contains(got, "not a registered Site") {
		t.Fatalf("prompt: %s", got)
	}
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "ARCHWRIGHT_SITE=") {
			t.Fatal("no Site hint expected")
		}
	}
}

func TestBuildRefusesUnknownRunner(t *testing.T) {
	if _, err := Build(Input{Runner: "grok"}); err == nil {
		t.Fatal("expected refusal")
	}
}

func TestSpoolRootPerRunner(t *testing.T) {
	if got := SpoolRoot(RunnerClaude, 501, "/var/tmp"); got != "/tmp/claude-501/archwright" {
		t.Fatal(got)
	}
	if got := SpoolRoot(RunnerCodex, 501, "/var/tmp"); got != "/var/tmp/archwright-501" {
		t.Fatal(got)
	}
}

func fakeCall(items string, err error) Call {
	return func(tool string, _ json.RawMessage) (json.RawMessage, error) {
		if err != nil {
			return nil, err
		}
		if tool == "list_jobs" {
			return json.RawMessage(`[{"id":"j1","title":"Fix login"}]`), nil
		}
		return json.RawMessage(items), nil
	}
}

func TestSessionStartRehydratesJobsAndInbox(t *testing.T) {
	var out bytes.Buffer
	items := `{"items":[{"id":1,"name":"instruct","job_id":"j1","summary":"The developer says: hurry"}]}`
	if err := Hook("session-start", fakeCall(items, nil), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{HooksMarker, "Fix login", InboxMarker, "hurry", "SessionStart"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
}

func TestInboxHooksDeliverOnlyWhenThereAreItems(t *testing.T) {
	var out bytes.Buffer
	for _, ev := range []string{"prompt", "stop"} {
		out.Reset()
		if err := Hook(ev, fakeCall(`{"items":[]}`, nil), &out); err != nil || out.Len() != 0 {
			t.Fatalf("%s empty inbox: %q %v", ev, out.String(), err)
		}
	}
	items := `{"items":[{"name":"cancel_job","summary":"cancelled","job_id":"j1"}]}`
	Hook("prompt", fakeCall(items, nil), &out)
	if !strings.Contains(out.String(), `"additionalContext"`) || !strings.Contains(out.String(), "cancelled") {
		t.Fatal(out.String())
	}
	out.Reset()
	Hook("stop", fakeCall(items, nil), &out)
	if !strings.Contains(out.String(), `"decision":"block"`) {
		t.Fatal(out.String())
	}
}

func TestHooksNeverFailTheSessionWhenWorksIsDown(t *testing.T) {
	var out bytes.Buffer
	down := errors.New("works_unreachable")
	for _, ev := range []string{"prompt", "stop"} {
		if err := Hook(ev, fakeCall("", down), &out); err != nil || out.Len() != 0 {
			t.Fatalf("%s: %q %v", ev, out.String(), err)
		}
	}
	if err := Hook("session-start", fakeCall("", down), &out); err != nil || !strings.Contains(out.String(), "poll") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := Hook("bogus", fakeCall("", nil), &out); err == nil {
		t.Fatal("unknown hook must error")
	}
}

func TestHomeIsUnderState(t *testing.T) {
	if got := Home(paths.Dirs{State: "/s"}); got != filepath.Join("/s", "home") {
		t.Fatal(got)
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func contains(s []string, v string) bool { return indexOf(s, v) >= 0 }
