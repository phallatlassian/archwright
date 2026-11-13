package works

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Brief is what a Warden gives a Wright through dispatch_wright (ADR 0006).
// The Works rejects a brief missing any required field: a Wright knows only
// its brief and will confidently do the wrong thing if it is wrong.
type Brief struct {
	Objective string `json:"objective"`
	Scope     struct {
		In  []string `json:"in"`
		Out []string `json:"out"`
	} `json:"scope"`
	// Constraints is a pointer so that an absent field (rejected) differs from
	// an empty list (stated: there are none).
	Constraints *[]string `json:"constraints"`
	// ReadOnly declares that the Wright writes nothing, so its scope cannot
	// conflict with another Job's. Absent means the piece writes.
	ReadOnly         bool     `json:"read_only,omitempty"`
	Inputs           []string `json:"inputs"` // pointers to what the Wright needs, not the content
	DefinitionOfDone []string `json:"definition_of_done"`
	ReturnShape      string   `json:"return_shape"`
	Limits           struct {
		TimeoutMinutes int `json:"timeout_minutes"`
		MaxTurns       int `json:"max_turns"`
	} `json:"limits"`
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

func blankList(l []string) bool {
	for _, s := range l {
		if !blank(s) {
			return false
		}
	}
	return true
}

// Validate reports every missing or empty required field at once.
func (b Brief) Validate() error {
	var missing []string
	if blank(b.Objective) {
		missing = append(missing, "objective")
	}
	if blankList(b.Scope.In) {
		missing = append(missing, "scope.in")
	}
	if b.Constraints == nil {
		missing = append(missing, "constraints (state it, even if empty)")
	}
	if blankList(b.Inputs) {
		missing = append(missing, "inputs")
	}
	if blankList(b.DefinitionOfDone) {
		missing = append(missing, "definition_of_done")
	}
	if blank(b.ReturnShape) {
		missing = append(missing, "return_shape")
	}
	if b.Limits.TimeoutMinutes <= 0 {
		missing = append(missing, "limits.timeout_minutes")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: brief is incomplete, missing: %s", errInvalidInput, strings.Join(missing, ", "))
	}
	return nil
}

func parseBrief(raw json.RawMessage) (Brief, error) {
	var b Brief
	if len(raw) == 0 {
		return b, fmt.Errorf("%w: brief is required", errInvalidInput)
	}
	if err := decode(raw, &b); err != nil {
		return b, err
	}
	return b, b.Validate()
}

func bullets(w *strings.Builder, items []string) {
	if len(items) == 0 {
		w.WriteString("- (none)\n")
	}
	for _, it := range items {
		fmt.Fprintf(w, "- %s\n", it)
	}
}

// Render writes the brief the Wright receives: the Warden's fields, the
// Guild's standards, and how to return the result. round and reason are set
// for a rework, which is a fresh run (ADR 0003) and so is told what was wrong.
func (b Brief) Render(guild, standards string, round int, reason string) string {
	var w strings.Builder
	w.WriteString("# Brief\n\n")
	if round > 0 {
		fmt.Fprintf(&w, "## Rework round %d\n\nA previous attempt at this piece was rejected: %s\n\n", round, reason)
	}
	fmt.Fprintf(&w, "## Objective\n\n%s\n\n## Scope\n\nIn:\n", b.Objective)
	bullets(&w, b.Scope.In)
	w.WriteString("\nOut:\n")
	bullets(&w, b.Scope.Out)
	w.WriteString("\n## Constraints\n\n")
	bullets(&w, *b.Constraints)
	w.WriteString("\n## Inputs\n\n")
	bullets(&w, b.Inputs)
	w.WriteString("\n## Definition of done\n\n")
	bullets(&w, b.DefinitionOfDone)
	fmt.Fprintf(&w, "\n## Return shape\n\n%s\n\n## Limits\n\n- timeout: %d minutes\n", b.ReturnShape, b.Limits.TimeoutMinutes)
	if b.Limits.MaxTurns > 0 {
		fmt.Fprintf(&w, "- max turns: %d\n", b.Limits.MaxTurns)
	}
	fmt.Fprintf(&w, "\n## %s Guild standards\n\n", guild)
	if s := strings.TrimSpace(standards); s != "" {
		w.WriteString(s + "\n")
	} else {
		w.WriteString("(the Guild states no standards)\n")
	}
	w.WriteString("\n## Returning your result\n\nWhen finished, call `archwright tool report_result` with JSON on stdin:\n" +
		"`{\"status\":\"done|blocked|failed\",\"result\":\"<pointer to the result>\",\"evidence\":[...],\"observations\":[...],\"uncertainty\":\"...\"}`.\n" +
		"Return large artifacts by pointer. Put anything outside your remit under `observations`.\n" +
		"\n## Asking\n\nIf you need a human decision, a manual step done, or something clarified, call `archwright tool ask` with JSON on stdin:\n" +
		"`{\"kind\":\"decision|manual-step|clarification\",\"blocking\":true,\"why\":\"...\",\"questions\":[{\"id\":\"q1\",\"text\":\"...\",\"options\":[...],\"default\":\"<recommended option>\"}]}`.\n" +
		"With `blocking` true, stop right after the call and end your run without reporting a result: your task is parked, and a fresh run starts with this brief plus the answers. " +
		"A manual step is a checklist the developer confirms. Never ask for a secret or put one in an ask.\n")
	return w.String()
}
