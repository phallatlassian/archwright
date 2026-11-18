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

const issueURL = "https://github.com/acme/app/issues/7"

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

func TestGHReadsTheIssueThroughGh(t *testing.T) {
	log := stubGH(t, "ok")
	is, err := GH{}.ReadIssue(context.Background(), issueURL)
	if err != nil {
		t.Fatal(err)
	}
	if is.Title != "Login fails" || is.State != "OPEN" || is.Author != "octo" || !strings.Contains(is.Body, "log in") ||
		len(is.Labels) != 2 || is.Labels[0] != "bug" || is.URL != issueURL {
		t.Fatalf("%+v", is)
	}
	calls, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(calls)); !strings.HasPrefix(got, "issue view "+issueURL+" --json ") {
		t.Fatalf("gh called as %q", got)
	}
}

func TestGHClassifiesFaultsAndDoesNotRetry(t *testing.T) {
	for _, c := range []struct{ mode, kind string }{
		{"auth", KindAuth}, {"network", KindNetwork}, {"notfound", KindOther}, {"garbage", KindOther},
	} {
		log := stubGH(t, c.mode)
		_, err := GH{}.ReadIssue(context.Background(), issueURL)
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
	_, err := GH{Timeout: 100 * time.Millisecond}.ReadIssue(context.Background(), issueURL)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindNetwork {
		t.Fatalf("%v", err)
	}
}

func TestGHMissingIsAnOtherFault(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := GH{}.ReadIssue(context.Background(), issueURL)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindOther || !strings.Contains(fe.Detail, "not installed") {
		t.Fatalf("%v", err)
	}
}

func TestUnsupportedAnswersUnsupported(t *testing.T) {
	if _, err := (Unsupported{}).ReadIssue(context.Background(), issueURL); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestDefaultRoutesByHost(t *testing.T) {
	stubGH(t, "ok")
	if _, err := Default().ReadIssue(context.Background(), issueURL); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://gitlab.example.com/acme/app/issues/7", "::bad"} {
		if _, err := Default().ReadIssue(context.Background(), u); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s: %v", u, err)
		}
	}
}
