package works

import "encoding/json"

// acceptPiece records that the Warden accepted a Wright's finished piece. It
// merges nothing: the Warden merges the piece branch into the Job's
// integration branch itself, in its own worktree, before it calls this, and
// sends a conflicting piece back to its Wright with rework_wright (ADR 0030).
// The Works still runs the Site's gates on a Snapshot at handback, outside any
// agent (ADR 0028).
func acceptPiece(c *callCtx, in json.RawMessage) (any, error) {
	var a struct {
		Piece string `json:"piece"`
	}
	if err := decode(in, &a); err != nil {
		return nil, err
	}
	if _, err := c.warden(); err != nil {
		return nil, err
	}
	p, err := c.pieceOf(a.Piece)
	if err != nil {
		return nil, err
	}
	if err := c.st.AcceptPiece(p.JobID, p.Piece, resultIsDone); err != nil {
		return nil, err
	}
	return c.st.GetPiece(p.JobID, p.Piece)
}
