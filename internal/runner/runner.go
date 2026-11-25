// Package runner holds what every vendor CLI the Works hosts shares (ADR 0026):
// the permission posture a Session starts under, how it ended, and the
// hard-coded never-list of ADR 0027 (guard.go) that refuses a launch asking
// for more than the developer's own default. Sessions themselves are package
// session's.
package runner

// Posture is the permission posture a Session started under, recorded as its
// first event (ADR 0027).
type Posture struct {
	Runner  string   `json:"runner"`
	Version string   `json:"version"`
	Mode    string   `json:"mode"`    // the permission mode requested
	Sandbox string   `json:"sandbox"` // always SandboxInherited: the factory never changes it
	Flags   []string `json:"flags"`   // every flag passed, the brief excluded
	Notices []string `json:"notices"` // notices the launcher saw, such as managed policy
}

// Status is how a Session ended.
type Status string

const (
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Spawn is what a caller may ask of a Session's permission posture. Everything
// in it is checked against the never-list; none of it can widen permissions.
type Spawn struct {
	Mode           string   // "", default, plan or dontAsk
	ExtraArgs      []string // extra runner flags
	Settings       string   // a --settings override
	MCPConfig      string   // an --mcp-config override
	AllowRules     []string // allow rules to add
	SettingsWrites []string // settings files to write
	DisableSandbox bool
}

// EffectiveMode is the permission mode the Session will have.
func (s Spawn) EffectiveMode() string {
	if s.Mode == "" {
		return ModeDefault
	}
	return s.Mode
}

// SandboxInherited is the sandbox posture of every Session: the factory never changes it.
const SandboxInherited = "inherited"
