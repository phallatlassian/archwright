package forge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const prURL = "https://github.com/acme/app/pull/12"

func openReq() OpenPRRequest {
	return OpenPRRequest{SiteURL: "git@github.com:acme/app.git", Head: "archwright/job-1/integration", Base: "main", Title: "Fix login", Body: "Closes #7"}
}

// stubPRState gives the stub gh a state directory and returns it.
func stubPRState(t *testing.T) string {
	t.Helper()
	st := t.TempDir()
	t.Setenv("STUB_GH_STATE", st)
	return st
}

func TestGHOpenPRCreatesOnceAndReturnsTheSamePRTheSecondTime(t *testing.T) {
	log := stubGH(t, "ok")
	st := stubPRState(t)
	first, err := GH{}.OpenPR(context.Background(), openReq())
	if err != nil {
		t.Fatal(err)
	}
	if first.URL != prURL || first.Number != 12 || first.Existing {
		t.Fatalf("first: %+v", first)
	}
	second, err := GH{}.OpenPR(context.Background(), openReq())
	if err != nil {
		t.Fatal(err)
	}
	if second.URL != first.URL || !second.Existing {
		t.Fatalf("second: %+v", second)
	}
	calls, _ := os.ReadFile(log)
	if n := strings.Count(string(calls), "pr create"); n != 1 {
		t.Fatalf("pr create ran %d times:\n%s", n, calls)
	}
	body, _ := os.ReadFile(filepath.Join(st, "body"))
	if string(body) != "Closes #7" {
		t.Fatalf("body on stdin: %q", body)
	}
}

func TestGHOpenPRFixesHeadAndBaseAndTheRepositoryFromTheRequest(t *testing.T) {
	log := stubGH(t, "ok")
	stubPRState(t)
	if _, err := (GH{}).OpenPR(context.Background(), openReq()); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 {
		t.Fatalf("calls: %v", lines)
	}
	want := "pr create -R github.com/acme/app --head archwright/job-1/integration --base main --title=Fix login --body-file -"
	if lines[1] != want {
		t.Fatalf("create\n got %s\nwant %s", lines[1], want)
	}
	if !strings.HasPrefix(lines[0], "pr list -R github.com/acme/app --head archwright/job-1/integration --base main --state open") {
		t.Fatalf("lookup: %s", lines[0])
	}
}

func TestGHOpenPRRefusesWhatIsNotAPlainHeadAndBase(t *testing.T) {
	log := stubGH(t, "ok")
	stubPRState(t)
	for name, mutate := range map[string]func(*OpenPRRequest){
		"no head":      func(r *OpenPRRequest) { r.Head = "" },
		"no base":      func(r *OpenPRRequest) { r.Base = "" },
		"head is base": func(r *OpenPRRequest) { r.Head = "main" },
		"option head":  func(r *OpenPRRequest) { r.Head = "--web" },
		"bad site":     func(r *OpenPRRequest) { r.SiteURL = "/tmp/local" },
	} {
		r := openReq()
		mutate(&r)
		if _, err := (GH{}).OpenPR(context.Background(), r); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 {
		t.Fatalf("gh was run: %s", calls)
	}
}

func TestGHReadPRIncludesCIChecks(t *testing.T) {
	log := stubGH(t, "ok")
	stubPRState(t)
	pr, err := GH{}.ReadPR(context.Background(), prURL)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Title != "Fix login" || pr.State != "OPEN" || pr.Head != "archwright/job-1/integration" || pr.Base != "main" || pr.Mergeable != "MERGEABLE" {
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
	if !strings.HasPrefix(string(calls), "pr view "+prURL+" --json ") || !strings.Contains(string(calls), "statusCheckRollup") {
		t.Fatalf("gh called as %q", calls)
	}
}

func TestGHCommentPRSendsTheBodyOnStdin(t *testing.T) {
	log := stubGH(t, "ok")
	st := stubPRState(t)
	body := "--web $(rm -rf /)\nsecond line"
	if err := (GH{}).CommentPR(context.Background(), prURL, body); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(calls)); got != "pr comment "+prURL+" --body-file -" {
		t.Fatalf("gh called as %q", got)
	}
	got, _ := os.ReadFile(filepath.Join(st, "comments"))
	if strings.TrimSpace(string(got)) != body {
		t.Fatalf("comment: %q", got)
	}
}

func TestGHPRCallsRefuseANonPullRequestURL(t *testing.T) {
	log := stubGH(t, "ok")
	for _, u := range []string{"--web", "https://github.com/acme/app/issues/7", prURL + " --admin", "12"} {
		if _, err := (GH{}).ReadPR(context.Background(), u); err == nil {
			t.Errorf("read %q", u)
		}
		if err := (GH{}).CommentPR(context.Background(), u, "x"); err == nil {
			t.Errorf("comment %q", u)
		}
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 {
		t.Fatalf("gh was run: %s", calls)
	}
}

func TestGHPRCallsClassifyFaults(t *testing.T) {
	for _, c := range []struct{ mode, kind string }{{"auth", KindAuth}, {"network", KindNetwork}, {"notfound", KindOther}} {
		stubGH(t, c.mode)
		var fe *Error
		if _, err := (GH{}).OpenPR(context.Background(), openReq()); !errors.As(err, &fe) || fe.Kind != c.kind {
			t.Errorf("open %s: %v", c.mode, err)
		}
		if _, err := (GH{}).ReadPR(context.Background(), prURL); !errors.As(err, &fe) || fe.Kind != c.kind {
			t.Errorf("read %s: %v", c.mode, err)
		}
		if err := (GH{}).CommentPR(context.Background(), prURL, "x"); !errors.As(err, &fe) || fe.Kind != c.kind {
			t.Errorf("comment %s: %v", c.mode, err)
		}
	}
}

func TestByHostRoutesPRCallsAndRefusesUnknownHosts(t *testing.T) {
	stubGH(t, "ok")
	stubPRState(t)
	f := Default()
	if _, err := f.OpenPR(context.Background(), openReq()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadPR(context.Background(), prURL); err != nil {
		t.Fatal(err)
	}
	other := "https://gitlab.example.com/acme/app/pull/12"
	r := openReq()
	r.SiteURL = "https://gitlab.example.com/acme/app.git"
	if _, err := f.OpenPR(context.Background(), r); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := f.ReadPR(context.Background(), other); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if err := f.CommentPR(context.Background(), other, "x"); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
