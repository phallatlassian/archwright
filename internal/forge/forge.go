// Package forge is the seam to the service that hosts a Site's repository,
// issues and pull requests (ADR 0022). The Works owns the interface and performs
// every Forge action itself; agents never reach the Forge directly.
package forge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Issue is a Forge issue as read. Everything in it is third-party text.
type Issue struct {
	URL    string   `json:"url"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	State  string   `json:"state,omitempty"`
	Author string   `json:"author,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// Forge is what the Works asks of a Forge. A second Forge is a second adapter.
type Forge interface {
	// ReadIssue reads the issue at the given issue URL.
	ReadIssue(ctx context.Context, issueURL string) (Issue, error)
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
// the developer (auth, network) or is only the agent's to report.
type Error struct {
	Kind   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("forge %s fault: %s", e.Kind, e.Detail) }

// NeedsDeveloper reports whether the developer must act before a retry can work.
func (e *Error) NeedsDeveloper() bool { return e.Kind == KindAuth || e.Kind == KindNetwork }

// Unsupported is the no-op Forge: it answers "unsupported" to everything.
type Unsupported struct{}

// ReadIssue always returns ErrUnsupported.
func (Unsupported) ReadIssue(context.Context, string) (Issue, error) { return Issue{}, ErrUnsupported }

// ByHost routes each call to the adapter for the URL's host; a host with no
// adapter is answered "unsupported".
type ByHost struct {
	Adapters map[string]Forge
}

// Default is the Forge the Works runs with: GitHub through the developer's gh.
func Default() Forge {
	return ByHost{Adapters: map[string]Forge{"github.com": GH{}}}
}

// ReadIssue implements Forge.
func (b ByHost) ReadIssue(ctx context.Context, issueURL string) (Issue, error) {
	return b.pick(issueURL).ReadIssue(ctx, issueURL)
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
