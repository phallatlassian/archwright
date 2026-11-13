package works

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phallatlassian/archwright/internal/store"
)

// Wright result statuses.
const (
	ResultDone    = "done"
	ResultBlocked = "blocked"
	ResultFailed  = "failed"
)

// WrightResult is what a Wright returns through report_result (ADR 0006).
type WrightResult struct {
	Status       string   `json:"status"`
	Result       string   `json:"result"` // pointer to the result; large artifacts are never inlined
	Evidence     []string `json:"evidence"`
	Observations []string `json:"observations"` // out-of-remit, for the Warden
	Uncertainty  string   `json:"uncertainty"`
}

// Validate checks the result's shape.
func (r WrightResult) Validate() error {
	switch r.Status {
	case ResultDone:
		var missing []string
		if blank(r.Result) {
			missing = append(missing, "result")
		}
		if blankList(r.Evidence) {
			missing = append(missing, "evidence")
		}
		if len(missing) > 0 {
			return fmt.Errorf("%w: a done result needs %s", errInvalidInput, strings.Join(missing, " and "))
		}
	case ResultBlocked, ResultFailed:
		if blank(r.Uncertainty) && blankList(r.Observations) {
			return fmt.Errorf("%w: a %s result must say why, in uncertainty or observations", errInvalidInput, r.Status)
		}
	default:
		return fmt.Errorf("%w: status must be done, blocked or failed", errInvalidInput)
	}
	return nil
}

func resultIsDone(raw json.RawMessage) bool {
	var r WrightResult
	return json.Unmarshal(raw, &r) == nil && r.Status == ResultDone
}

// Handback statuses.
const (
	HandbackDone    = "done"
	HandbackPartial = "partial"
	HandbackFailed  = "failed"
)

// Flag kinds a Warden may raise. Flags go to the Archwright only; Guilds
// never commission each other.
var flagKinds = map[string]bool{"follow-up": true, "decision-needed": true, "risk": true}

// FlagInput is a flag as the Warden writes it.
type FlagInput struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Evidence    string `json:"evidence"` // pointer to the evidence
}

// HandbackInput is what a Warden returns through handback. Gate results are
// not part of it: the Works records them itself.
type HandbackInput struct {
	Status      string      `json:"status"`
	Results     []string    `json:"results"`  // pointers to the accepted results
	Checked     []string    `json:"checked"`  // what the Warden inspected
	Rejected    []string    `json:"rejected"` // what it rejected, and why
	Uncertainty string      `json:"uncertainty"`
	Flags       []FlagInput `json:"flags"`
}

// Validate checks the handback's shape and its agreement with the Job's pieces.
func (h HandbackInput) Validate(pieces []store.Piece) error {
	var missing []string
	if blankList(h.Checked) {
		missing = append(missing, "checked")
	}
	switch h.Status {
	case HandbackDone, HandbackPartial:
		if blankList(h.Results) {
			missing = append(missing, "results")
		}
	case HandbackFailed:
		if blank(h.Uncertainty) && blankList(h.Rejected) {
			missing = append(missing, "uncertainty or rejected (say why)")
		}
	default:
		return fmt.Errorf("%w: status must be done, partial or failed", errInvalidInput)
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: handback is missing %s", errInvalidInput, strings.Join(missing, ", "))
	}
	for i, f := range h.Flags {
		if !flagKinds[f.Kind] {
			return fmt.Errorf("%w: flag %d: kind must be follow-up, decision-needed or risk", errInvalidInput, i+1)
		}
		if blank(f.Description) || blank(f.Evidence) {
			return fmt.Errorf("%w: flag %d: description and evidence are required", errInvalidInput, i+1)
		}
	}
	accepted := 0
	for _, p := range pieces {
		if p.Status == store.PieceAccepted {
			accepted++
		}
	}
	switch {
	case len(pieces) == 0:
		return fmt.Errorf("%w: nothing was dispatched for this Job", errInvalidInput)
	case h.Status == HandbackDone && accepted != len(pieces):
		return fmt.Errorf("%w: status done, but %d of %d pieces are not accepted", errInvalidInput, len(pieces)-accepted, len(pieces))
	case h.Status == HandbackPartial && (accepted == 0 || accepted == len(pieces)):
		return fmt.Errorf("%w: status partial needs some pieces accepted and some not (%d of %d accepted)", errInvalidInput, accepted, len(pieces))
	case h.Status == HandbackFailed && accepted > 0:
		return fmt.Errorf("%w: status failed, but %d pieces are accepted; hand back partial", errInvalidInput, accepted)
	}
	return nil
}

// HandbackBody is what is stored and shown with a handback; the flags are
// stored apart, for the Archwright's list_flags only.
type HandbackBody struct {
	Status      string        `json:"status"`
	Results     []string      `json:"results"`
	Checked     []string      `json:"checked"`
	Rejected    []string      `json:"rejected"`
	Uncertainty string        `json:"uncertainty"`
	Pieces      []store.Piece `json:"pieces"`
}
