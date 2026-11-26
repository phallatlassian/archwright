package store

import (
	"strings"
	"testing"
)

func TestCreateJobFromIssueRef(t *testing.T) {
	cases := []struct{ name, remote, ref, want, wantErr string }{
		{"ssh number", "git@github.com:o/r.git", "7", "https://github.com/o/r/issues/7", ""},
		{"https hash", "https://github.com/o/r", "#7", "https://github.com/o/r/issues/7", ""},
		{"https .git", "https://github.com/o/r.git", "7", "https://github.com/o/r/issues/7", ""},
		{"enterprise", "git@ghe.example.com:team/app.git", "12", "https://ghe.example.com/team/app/issues/12", ""},
		{"owner repo", "git@github.com:o/r.git", "x/y#3", "https://github.com/x/y/issues/3", ""},
		{"ssh port", "ssh://git@host:2222/o/r.git", "7", "", "full"},
		{"bad ref", "https://github.com/o/r", "seven", "", "issue reference"},
		{"empty owner", "https://github.com/o/r", "/y#3", "", "issue reference"},
		{"empty repo", "https://github.com/o/r", "x/#3", "", "issue reference"},
		{"zero","https://github.com/o/r", "#0x", "", "issue reference"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := open(t)
			addSite(t, s, "r", c.remote)
			j, err := s.CreateJobFromRef("r", "t", "", "", c.ref)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil || j.SourceIssue != c.want {
				t.Fatalf("got %q, %v; want %q", j.SourceIssue, err, c.want)
			}
		})
	}
}

func TestCreateJobFromIssueRefNeedsSite(t *testing.T) {
	s, _ := open(t)
	addSite(t, s, "alpha", "https://github.com/o/a")
	addSite(t, s, "beta", "https://github.com/o/b")
	_, err := s.CreateJobFromRef("", "t", "", "", "#1")
	if err == nil || !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("err = %v, want Site names", err)
	}
	if _, err := s.CreateJobFromRef("alpha", "t", "", "https://github.com/o/a/issues/1", "1"); err == nil {
		t.Fatal("both source_issue and issue accepted")
	}
}
