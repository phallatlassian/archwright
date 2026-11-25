package forge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const prURL = "https://github.com/acme/app/pull/12"

// stubGH puts the stub gh first on PATH as `gh`, in the given mode, and
// returns the file its calls are logged to.
func stubGH(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	src, err := os.ReadFile("testdata/stub-gh.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls.log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_GH_MODE", mode)
	t.Setenv("STUB_GH_LOG", log)
	return log
}

func TestGHReadPRIncludesCIChecks(t *testing.T) {
	log := stubGH(t, "ok")
	pr, err := GH{}.ReadPR(context.Background(), prURL)
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != "OPEN" || pr.Head != "archwright/job-1/integration" || pr.Base != "main" || pr.Mergeable != "MERGEABLE" || pr.URL != prURL || pr.Number != 12 {
		t.Fatalf("%+v", pr)
	}
	want := []Check{
		{Name: "build", Status: "completed", Conclusion: "success", URL: "https://ci/1"},
		{Name: "test", Status: "in_progress", URL: "https://ci/2"},
		{Name: "lint", Status: "completed", Conclusion: "failure", URL: "https://ci/3"},
	}
	if len(pr.Checks) != len(want) {
		t.Fatalf("checks: %+v", pr.Checks)
	}
	for i, c := range want {
		if pr.Checks[i] != c {
			t.Errorf("check %d: got %+v want %+v", i, pr.Checks[i], c)
		}
	}
	calls, _ := os.ReadFile(log)
	if !strings.HasPrefix(string(calls), "pr view "+prURL+" --json ") || !strings.Contains(string(calls), "statusCheckRollup") || strings.Count(string(calls), "\n") != 1 {
		t.Fatalf("gh called as %q", calls)
	}
}

func TestGHReadPRRefusesANonPullRequestURL(t *testing.T) {
	log := stubGH(t, "ok")
	for _, u := range []string{"--web", "https://github.com/acme/app/issues/7", prURL + " --admin", "12"} {
		if _, err := (GH{}).ReadPR(context.Background(), u); err == nil {
			t.Errorf("read %q", u)
		}
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 {
		t.Fatalf("gh was run: %s", calls)
	}
}

func TestGHClassifiesFaultsAndDoesNotRetry(t *testing.T) {
	for _, c := range []struct{ mode, kind string }{
		{"auth", KindAuth}, {"network", KindNetwork}, {"notfound", KindOther}, {"garbage", KindOther},
	} {
		log := stubGH(t, c.mode)
		_, err := GH{}.ReadPR(context.Background(), prURL)
		var fe *Error
		if !errors.As(err, &fe) || fe.Kind != c.kind {
			t.Fatalf("%s: %v", c.mode, err)
		}
		if fe.NeedsDeveloper() != (c.kind != KindOther) {
			t.Fatalf("%s: NeedsDeveloper = %v", c.mode, fe.NeedsDeveloper())
		}
		if calls, _ := os.ReadFile(log); strings.Count(string(calls), "\n") != 1 {
			t.Fatalf("%s: gh called more than once: %q", c.mode, calls)
		}
	}
}

func TestGHHangIsANetworkFault(t *testing.T) {
	stubGH(t, "hang")
	_, err := GH{Timeout: 100 * time.Millisecond}.ReadPR(context.Background(), prURL)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindNetwork {
		t.Fatalf("%v", err)
	}
}

func TestGHMissingIsAnOtherFault(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := GH{}.ReadPR(context.Background(), prURL)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindOther || !strings.Contains(fe.Detail, "not installed") {
		t.Fatalf("%v", err)
	}
}

func TestUnsupportedAnswersUnsupported(t *testing.T) {
	if _, err := (Unsupported{}).ReadPR(context.Background(), prURL); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestDefaultRoutesByHostAndRefusesUnknownHosts(t *testing.T) {
	stubGH(t, "ok")
	if _, err := Default().ReadPR(context.Background(), prURL); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://gitlab.example.com/acme/app/pull/12", "::bad"} {
		if _, err := Default().ReadPR(context.Background(), u); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s: %v", u, err)
		}
	}
}

func TestSupportsSiteByHost(t *testing.T) {
	for url, want := range map[string]bool{
		"git@github.com:acme/app.git": true, "https://github.com/acme/app": true,
		"https://gitlab.example.com/acme/app.git": false, "/tmp/local": false,
	} {
		if SupportsSite(url) != want {
			t.Errorf("%s: want %v", url, want)
		}
	}
}

func TestParsePRURLNamesTheRepositoryAndNumber(t *testing.T) {
	for u, want := range map[string]PRRef{
		prURL:                                     {"github.com/acme/app", 12},
		"https://GitHub.com/Acme/App/pull/7":      {"github.com/Acme/App", 7},
		"http://git.example.com/o/r/pull/100":     {"git.example.com/o/r", 100},
		"https://git.example.com:8443/o/r/pull/3": {"git.example.com:8443/o/r", 3},
	} {
		got, err := ParsePRURL(u)
		if err != nil || got != want {
			t.Errorf("%s: %+v, %v (want %+v)", u, got, err, want)
		}
	}
}

func TestParsePRURLRefusesWhatIsNotExactlyAPullRequestURL(t *testing.T) {
	for _, u := range []string{
		"", "12", "--web", prURL + " --admin", prURL + "/", prURL + "/files", prURL + "?x=1", prURL + "#c", prURL + "?", prURL + "#",
		"https://github.com/acme/app/issues/12", "https://github.com/acme/pull/12", "https://github.com/acme/app/extra/pull/12",
		"https://github.com/acme/app/pull/0", "https://github.com/acme/app/pull/012", "https://github.com/acme/app/pull/-1", "https://github.com/acme/app/pull/x",
		"ssh://git@github.com/acme/app/pull/12", "file:///acme/app/pull/12", "git@github.com:acme/app/pull/12",
		"https://user:pw@github.com/acme/app/pull/12", "https:///acme/app/pull/12", "https://github.com//app/pull/12",
	} {
		if got, err := ParsePRURL(u); err == nil {
			t.Errorf("%q parsed as %+v", u, got)
		}
	}
}
