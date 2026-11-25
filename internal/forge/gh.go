package forge

import (
	"strings"
	"time"
)

// defaultTimeout bounds one gh call; a hang is reported as a network fault.
const defaultTimeout = 30 * time.Second

// GH is the adapter that shells out to the developer's own gh, reusing its
// stored credentials so no secret enters Archwright's configuration. Bin is
// the gh executable; empty means "gh" found on PATH at call time. Timeout
// bounds one call; zero means 30 seconds.
type GH struct {
	Bin     string
	Timeout time.Duration
}

var (
	authMarkers = []string{"gh auth login", "not logged in", "authentication", "could not read username", "could not read password", "permission denied (publickey)", "http 401", "bad credentials", "gh_token"}
	netMarkers  = []string{"could not resolve host", "no such host", "dial tcp", "connection refused", "i/o timeout", "network is unreachable", "timeout", "tls", "x509", "unable to access", "could not read from remote repository"}
)

// Classify sorts the output of a failed gh call into a fault class, as the
// adapter does for its own calls.
func Classify(output string, err error) *Error { return classify(output, err) }

// classify sorts a failed gh call by its stderr.
func classify(stderr string, err error) *Error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = err.Error()
	}
	low := strings.ToLower(detail)
	for _, m := range authMarkers {
		if strings.Contains(low, m) {
			return &Error{Kind: KindAuth, Detail: detail}
		}
	}
	for _, m := range netMarkers {
		if strings.Contains(low, m) {
			return &Error{Kind: KindNetwork, Detail: detail}
		}
	}
	return &Error{Kind: KindOther, Detail: detail}
}
