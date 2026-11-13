package remote

import (
	"strings"
	"testing"
)

func TestNormalizeTreatsSpellingsOfOneRepoAsEqual(t *testing.T) {
	same := []string{
		"git@github.com:o/r.git",
		"git@github.com:o/r",
		"https://github.com/o/r",
		"https://github.com/o/r.git",
		"https://github.com/o/r.git/",
		"https://GitHub.com/o/r.git",
		"ssh://git@github.com/o/r.git",
		"ssh://git@github.com:22/o/r.git",
		"github.com:o/r",
		"git://github.com/o/r",
	}
	want, err := Normalize(same[0])
	if err != nil || want != "github.com/o/r" {
		t.Fatalf("%q %v", want, err)
	}
	for _, u := range same {
		if got, err := Normalize(u); err != nil || got != want {
			t.Errorf("%s: %q %v", u, got, err)
		}
	}
	for _, u := range []string{"https://github.com/o/other", "https://gitlab.com/o/r", "https://github.com:8443/o/r"} {
		if got, err := Normalize(u); err != nil || got == want {
			t.Errorf("%s: %q %v", u, got, err)
		}
	}
}

func TestNormalizeRefusesLocalAndMalformedURLs(t *testing.T) {
	for _, u := range []string{
		"", " ", "/srv/git/r.git", "./r", "../r", "r", "~/r", "file:///srv/git/r.git", "FILE://host/r",
		"-oProxyCommand=x", "--upload-pack=x", "https://", "https://host", "https://host/", "ext::sh -c x",
		"https://host/o/../r", "https://host/o/r with space", "ftp://host/o/r", "C:\\r",
	} {
		if got, err := Normalize(u); err == nil {
			t.Errorf("%q accepted as %q", u, got)
		}
	}
}

func TestName(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:o/My_Repo.git": "my_repo",
		"https://host/a/b/c.git":       "c",
		"https://host/x":               "x",
	} {
		if got := Name(in); got != want {
			t.Errorf("%s: %q want %q", in, got, want)
		}
	}
}

func TestNormalizeAllowsDotsInNames(t *testing.T) {
	if got, err := Normalize("https://host/o/foo..bar.git"); err != nil || got != "host/o/foo..bar" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestNormalizeRefusesEmbeddedCredentialsWithoutEchoingThem(t *testing.T) {
	for _, u := range []string{
		"https://user:s3cret@host/o/r", "https://s3cret@host/o/r", "http://s3cret@host/o/r",
		"git://s3cret@host/o/r", "ssh://git:s3cret@host/o/r", "git:s3cret@host:o/r",
	} {
		got, err := Normalize(u)
		if err == nil {
			t.Errorf("%q accepted as %q", u, got)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "credentials") {
			t.Errorf("%q: unhelpful or leaking error %q", u, err)
		}
	}
}

func TestNormalizeKeepsPlainSSHUsernames(t *testing.T) {
	for _, u := range []string{"git@host:o/r.git", "ssh://git@host/o/r.git", "https://host/o/r.git"} {
		if got, err := Normalize(u); err != nil || got != "host/o/r" {
			t.Errorf("%s: %q %v", u, got, err)
		}
	}
}
