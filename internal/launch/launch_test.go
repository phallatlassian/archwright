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
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
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
		if tool == "show_catalogue" {
			return json.RawMessage(catalogueJSON), nil
		}
		return json.RawMessage(items), nil
	}
}

const catalogueJSON = `{"guilds":[
 {"name":"survey","intake":"A question whose answer changes what gets built.","handback":"Findings with\nevidence.","access":"read-only","runners":["claude"],"source":"shipped"},
 {"name":"masonry","intake":"A specified change.","handback":"A pull request.","access":"read-write","runners":["claude"],"source":"shipped"}],
 "jigs":[{"name":"investigate","description":"Answer a question with cited evidence.","parameters":[{"name":"question","required":true},{"name":"depth","default":"shallow"}],
  "steps":[{"id":"investigate","guild":"survey","task":"LONG STEP TASK TEXT"}]}],
 "errors":[{"kind":"guild","name":"broken","error":"bad access"}]}`

func TestSessionStartCarriesTheCatalogueTrimmed(t *testing.T) {
	var out bytes.Buffer
	if err := Hook("session-start", fakeCall(`{"items":[]}`, nil), &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput struct{ AdditionalContext string }
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ctx := got.HookSpecificOutput.AdditionalContext
	for _, want := range []string{
		"survey", "read-only", "A question whose answer changes what gets built.", "Findings with evidence.",
		"masonry", "read-write",
		"investigate", "Answer a question with cited evidence.", "question (required)", "depth (default shallow)",
		"start_jig", "broken", "bad access",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("start context lacks %q:\n%s", want, ctx)
		}
	}
	if strings.Contains(ctx, "LONG STEP TASK TEXT") {
		t.Fatalf("Jig steps must be trimmed:\n%s", ctx)
	}
}

func TestSessionStartSurvivesAnUnavailableCatalogue(t *testing.T) {
	var out bytes.Buffer
	call := func(tool string, in json.RawMessage) (json.RawMessage, error) {
		if tool == "show_catalogue" {
			return nil, errors.New("no definitions are loaded")
		}
		return fakeCall(`{"items":[]}`, nil)(tool, in)
	}
	if err := Hook("session-start", call, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{HooksMarker, "Fix login", "show_catalogue", "no definitions are loaded"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
}

func TestInstructionsNameTheCatalogueTools(t *testing.T) {
	for _, hooks := range []bool{true, false} {
		got := Instructions("", hooks)
		for _, want := range []string{"show_catalogue", "start_jig", "start_step", "Guild name"} {
			if !strings.Contains(got, want) {
				t.Fatalf("hooks=%v: instructions lack %q: %s", hooks, want, got)
			}
		}
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

func TestPostToolHookInjectsUnreadItemsAsContextAndIsSilentWithNone(t *testing.T) {
	var out bytes.Buffer
	if err := Hook("post-tool", fakeCall(`{"items":[]}`, nil), &out); err != nil || out.Len() != 0 {
		t.Fatalf("empty inbox: %q %v", out.String(), err)
	}
	items := `{"items":[{"name":"step_handback","summary":"fix handed back","job_id":"j1"}]}`
	if err := Hook("post-tool", fakeCall(items, nil), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"hookEventName":"PostToolUse"`, `"additionalContext"`, InboxMarker, "fix handed back", "(Job j1)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	out.Reset()
	if err := Hook("post-tool", fakeCall("", errors.New("works_unreachable")), &out); err != nil || out.Len() != 0 {
		t.Fatalf("a Works that does not answer must not fail the session: %q %v", out.String(), err)
	}
}

func TestPostToolHookAsksForAtMostTheCapAndTheStopHookForEverything(t *testing.T) {
	var asked []json.RawMessage
	call := func(tool string, in json.RawMessage) (json.RawMessage, error) {
		asked = append(asked, in)
		return json.RawMessage(`{"items":[]}`), nil
	}
	Hook("post-tool", call, &bytes.Buffer{})
	Hook("stop", call, &bytes.Buffer{})
	var got struct {
		MaxChars int `json:"max_chars"`
	}
	if err := json.Unmarshal(asked[0], &got); err != nil || got.MaxChars <= 0 || got.MaxChars+len(inboxHeader) != PostToolCap {
		t.Fatalf("post-tool read_inbox input %s: %+v %v", asked[0], got, err)
	}
	if len(asked[1]) != 0 {
		t.Fatalf("the stop hook reads everything, got %s", asked[1])
	}
}

func TestSessionStartRegistersTheInboxSocketAndSaysSo(t *testing.T) {
	env := func(k string) string {
		return map[string]string{EnvMessagingSocket: "/tmp/cc/1.sock", EnvMessagingToken: "tok"}[k]
	}
	var registered json.RawMessage
	call := func(tool string, in json.RawMessage) (json.RawMessage, error) {
		if tool == "register_session" {
			registered = in
			return json.RawMessage(`{}`), nil
		}
		return fakeCall(`{"items":[]}`, nil)(tool, in)
	}
	var out bytes.Buffer
	if err := HookEnv("session-start", call, env, &out); err != nil {
		t.Fatal(err)
	}
	if string(registered) != `{"socket":"/tmp/cc/1.sock","token":"tok"}` || !strings.Contains(out.String(), NudgeMarker) {
		t.Fatalf("registered %s; start context %s", registered, out.String())
	}
}

func TestSessionStartWithoutASocketOrAnAcceptingWorksClaimsNoNudge(t *testing.T) {
	withSocket := func(k string) string { return map[string]string{EnvMessagingSocket: "/tmp/cc/1.sock"}[k] }
	refuse := func(tool string, in json.RawMessage) (json.RawMessage, error) {
		if tool == "register_session" {
			return nil, errors.New("forbidden")
		}
		return fakeCall(`{"items":[]}`, nil)(tool, in)
	}
	for name, c := range map[string]struct {
		call Call
		env  func(string) string
	}{
		"no socket":     {fakeCall(`{"items":[]}`, nil), func(string) string { return "" }},
		"no environ":    {fakeCall(`{"items":[]}`, nil), nil},
		"works refuses": {refuse, withSocket},
	} {
		var out bytes.Buffer
		if err := HookEnv("session-start", c.call, c.env, &out); err != nil || strings.Contains(out.String(), NudgeMarker) || !strings.Contains(out.String(), HooksMarker) {
			t.Fatalf("%s: %q %v", name, out.String(), err)
		}
	}
}

func TestHasUnreadFollowsTheMarkerFile(t *testing.T) {
	dir := t.TempDir()
	if HasUnread(dir) {
		t.Fatal("no marker, nothing unread")
	}
	if err := os.WriteFile(filepath.Join(dir, UnreadMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasUnread(dir) {
		t.Fatal("marker present")
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

func TestInstructionsRenderEachSiteAndHooksCombination(t *testing.T) {
	cases := []struct {
		site         string
		hooks        bool
		want, reject []string
	}{
		{"shop", true, []string{`registered Site "shop"`, `create_job {"site": "shop"}`, "Hooks deliver it", HooksMarker, InboxMarker, NudgeMarker, "wait_seconds"}, []string{"not a registered Site", "Nothing delivers it"}},
		{"", true, []string{"not a registered Site", "Hooks deliver it", HooksMarker}, []string{"registered Site \"", "Nothing delivers it"}},
		{"shop", false, []string{`registered Site "shop"`, "Nothing delivers it", "read_inbox", "wait_seconds"}, []string{"Hooks deliver it", HooksMarker, NudgeMarker, "not a registered Site"}},
		{"", false, []string{"not a registered Site", "Nothing delivers it"}, []string{"Hooks deliver it", HooksMarker, "registered Site \""}},
	}
	for _, c := range cases {
		got := Instructions(c.site, c.hooks)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("site=%q hooks=%v: missing %q in %s", c.site, c.hooks, w, got)
			}
		}
		for _, w := range c.reject {
			if strings.Contains(got, w) {
				t.Errorf("site=%q hooks=%v: unexpected %q in %s", c.site, c.hooks, w, got)
			}
		}
		if strings.Contains(got, "<!--") || strings.Contains(got, "{{") || !strings.HasPrefix(got, "You are the Archwright") {
			t.Errorf("site=%q hooks=%v: template leaked or header not stripped: %s", c.site, c.hooks, got)
		}
	}
}
