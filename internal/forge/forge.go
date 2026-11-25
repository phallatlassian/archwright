// Package forge is the seam to the service that hosts a Site's repository and
// pull requests. Agents reach the Forge themselves, as the developer (ADR
// 0028); the Works only reads the state of a Job's recorded PR, and checks the
// PR it is told about against the Site's remote.
package forge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/phallatlassian/archwright/internal/remote"
)

// Check is one CI check on a pull request.
type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`               // queued, in_progress or completed
	Conclusion string `json:"conclusion,omitempty"` // success, failure, ... once completed
	URL        string `json:"url,omitempty"`
}

// PullRequest is a Forge pull request as read. It carries no title or body:
// the Works has no use for third-party text.
type PullRequest struct {
	URL       string  `json:"url"`
	Number    int     `json:"number,omitempty"`
	State     string  `json:"state,omitempty"`
	Draft     bool    `json:"draft,omitempty"`
	Head      string  `json:"head,omitempty"`
	Base      string  `json:"base,omitempty"`
	Mergeable string  `json:"mergeable,omitempty"`
	Checks    []Check `json:"checks,omitempty"`
}

// Forge is what the Works asks of a Forge. A second Forge is a second adapter.
type Forge interface {
	// ReadPR reads the pull request at prURL, including its CI checks.
	ReadPR(ctx context.Context, prURL string) (PullRequest, error)
}

// ErrUnsupported is what a Forge with no adapter answers to every call.
var ErrUnsupported = errors.New("unsupported: no Forge adapter for this host")

// Fault classes.
const (
	KindAuth    = "auth"    // credentials missing or rejected
	KindNetwork = "network" // the Forge could not be reached
	KindOther   = "other"   // anything else: not found, malformed, gh missing
)

// Error is a failed Forge call, classified so the Works knows whether it needs
// the developer (auth, network) or is not something they can fix by signing in.
type Error struct {
	Kind   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("forge %s fault: %s", e.Kind, e.Detail) }

// NeedsDeveloper reports whether the developer must act before a retry can work.
func (e *Error) NeedsDeveloper() bool { return e.Kind == KindAuth || e.Kind == KindNetwork }

// Unsupported is the no-op Forge: it answers "unsupported" to everything.
type Unsupported struct{}

// ReadPR always returns ErrUnsupported.
func (Unsupported) ReadPR(context.Context, string) (PullRequest, error) {
	return PullRequest{}, ErrUnsupported
}

// ByHost routes each call to the adapter for the URL's host; a host with no
// adapter is answered "unsupported".
type ByHost struct {
	Adapters map[string]Forge
}

// Default is the Forge the Works runs with: GitHub through the developer's gh.
func Default() Forge {
	return ByHost{Adapters: map[string]Forge{"github.com": GH{}}}
}

// ReadPR implements Forge.
func (b ByHost) ReadPR(ctx context.Context, prURL string) (PullRequest, error) {
	return b.pick(prURL).ReadPR(ctx, prURL)
}

// SupportsSite reports whether the Default Forge has an adapter for the host
// of a Site's remote URL (https or scp-style).
func SupportsSite(siteURL string) bool {
	key, err := remote.Normalize(siteURL)
	if err != nil {
		return false
	}
	host, _, _ := strings.Cut(key, "/")
	_, ok := Default().(ByHost).Adapters[strings.ToLower(host)]
	return ok
}

func (b ByHost) pick(ref string) Forge {
	u, err := url.Parse(ref)
	if err != nil {
		return Unsupported{}
	}
	if f, ok := b.Adapters[strings.ToLower(u.Hostname())]; ok {
		return f
	}
	return Unsupported{}
}

// PRRef is what a pull request URL names: the repository, as the normalised
// remote identity (`host/owner/repo`), and the pull request's number.
type PRRef struct {
	Repo   string
	Number int
}

// ParsePRURL reads a pull request URL of the shape
// `https://host/owner/repo/pull/N` (`http` is accepted too, for a Site served
// that way), with nothing before or after it: no
// credentials, query, fragment, trailing path or other scheme. It is the one
// place a URL an agent reported becomes a repository and a number, so what it
// refuses never reaches `gh`.
func ParsePRURL(raw string) (PRRef, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.HasSuffix(raw, "?") || strings.HasSuffix(raw, "#") {
		return PRRef{}, fmt.Errorf("%q is not a pull request URL (want https://HOST/OWNER/REPO/pull/NUMBER, or http)", raw)
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" || strings.HasSuffix(u.EscapedPath(), "/") || parts[0] == "" || parts[1] == "" {
		return PRRef{}, fmt.Errorf("%q is not a pull request URL (want https://HOST/OWNER/REPO/pull/NUMBER, or http)", raw)
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n < 1 || strconv.Itoa(n) != parts[3] {
		return PRRef{}, fmt.Errorf("%q is not a pull request URL: %q is not a pull request number", raw, parts[3])
	}
	repo, err := remote.Normalize(u.Scheme + "://" + u.Host + "/" + parts[0] + "/" + parts[1])
	if err != nil {
		return PRRef{}, fmt.Errorf("%q is not a pull request URL: %v", raw, err)
	}
	return PRRef{Repo: repo, Number: n}, nil
}
