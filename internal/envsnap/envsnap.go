// Package envsnap is the developer's shell environment as the Works hands it
// to Sessions: captured from the `archwright` CLI's own environment and cut
// down to an allow-list, so a Session behaves like the developer's terminal
// without inheriting every variable the Works happened to start with
// (docs/spec/tmux-runtime.md).
package envsnap

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// allowed are the variables a Session takes from the developer's shell: what
// finds programs and a home, locale and terminal, proxies and certificate
// bundles, the agents' own sign-in and provider settings, and the Forge's
// credentials. Everything else is left behind.
var allowed = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TERM": true, "COLORTERM": true, "LANG": true, "LANGUAGE": true, "TZ": true, "TMPDIR": true,
	"EDITOR": true, "VISUAL": true, "PAGER": true,
	"SSH_AUTH_SOCK": true, "GIT_SSH_COMMAND": true, "GIT_ASKPASS": true,
	"GH_TOKEN": true, "GITHUB_TOKEN": true, "GH_HOST": true, "GH_ENTERPRISE_TOKEN": true, "GH_CONFIG_DIR": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true, "REQUESTS_CA_BUNDLE": true, "CURL_CA_BUNDLE": true,
	"NVM_DIR": true, "GOPATH": true, "GOROOT": true, "GOFLAGS": true, "GOPROXY": true, "CARGO_HOME": true, "RUSTUP_HOME": true,
	"CLAUDE_CONFIG_DIR": true, "ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "ANTHROPIC_BASE_URL": true, "ANTHROPIC_MODEL": true,
	"OPENAI_API_KEY": true, "OPENAI_BASE_URL": true,
	// A cloud provider is reached by profile and region; its secret keys are not passed on.
	"AWS_PROFILE": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true, "AWS_CONFIG_FILE": true, "AWS_SHARED_CREDENTIALS_FILE": true,
}

// allowedPrefixes admit families of variables: locale categories, the XDG
// base directories, and the settings that point Claude Code at a cloud provider.
var allowedPrefixes = []string{"LC_", "XDG_", "CLAUDE_CODE_", "GOOGLE_APPLICATION_", "ANTHROPIC_VERTEX_", "CLOUD_ML_"}

// Allowed reports whether name is a variable a Session takes from the
// developer's shell. The Works' own variables (ARCHWRIGHT_*, which carry a
// run's identity and are set by the adapters) and the developer's tmux
// (TMUX, TMUX_PANE) are never among them.
func Allowed(name string) bool {
	if name == "" {
		return false
	}
	if allowed[name] {
		return true
	}
	for _, p := range allowedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Capture is the allow-listed part of environ, a list of KEY=value strings.
func Capture(environ []string) map[string]string {
	out := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && Allowed(k) {
			out[k] = v
		}
	}
	return out
}

// Filter is the allow-listed part of snap.
func Filter(snap map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range snap {
		if Allowed(k) {
			out[k] = v
		}
	}
	return out
}

// Environ is a snapshot as KEY=value strings, in a stable order.
func Environ(snap map[string]string) []string {
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+snap[k])
	}
	return out
}

// Holder keeps the latest snapshot: the one the Works started with, replaced
// by each later CLI call's. A Session takes whatever is current when it spawns.
type Holder struct {
	mu   sync.Mutex
	snap map[string]string
}

// NewHolder starts with the allow-listed part of environ.
func NewHolder(environ []string) *Holder { return &Holder{snap: Capture(environ)} }

// Get is a copy of the current snapshot.
func (h *Holder) Get() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]string, len(h.snap))
	for k, v := range h.snap {
		out[k] = v
	}
	return out
}

// Set replaces the snapshot with snap's allow-listed variables: what a client
// sends is filtered again here, so the allow-list is the Works' own.
func (h *Holder) Set(snap map[string]string) {
	clean := Filter(snap)
	h.mu.Lock()
	h.snap = clean
	h.mu.Unlock()
}

// Encode is the snapshot as a header value, and Decode its inverse.
func Encode(snap map[string]string) string {
	b, _ := json.Marshal(snap)
	return base64.StdEncoding.EncodeToString(b)
}

// Decode reads a value made by Encode.
func Decode(s string) (map[string]string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
