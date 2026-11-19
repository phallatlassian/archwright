package works

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func init() {
	catalogue["commit"] = tool{roles: []string{RoleWright}, call: commit}
}

// Limits on what a Wright may put in a commit.
const (
	maxCommitMessage = 20000
	maxCommitPaths   = 500
)

// Committed is the answer to commit.
type Committed struct {
	Status string   `json:"status"` // "committed", or "nothing_to_commit" when nothing was staged
	Branch string   `json:"branch"`
	SHA    string   `json:"sha"`
	Files  []string `json:"files,omitempty"`
}

// commit stages and commits in the calling Wright's own worktree, because a
// sandboxed Codex Wright cannot write the git directory (ADR 0022). The
// worktree is the one the Works made for this run, never an argument: the
// Wright supplies only the message and, optionally, which paths to stage
// (default: everything its run changed that git is not hiding for it).
func commit(c *callCtx, in json.RawMessage) (any, error) {
	var args struct {
		Message string   `json:"message"`
		Paths   []string `json:"paths"`
	}
	if err := decode(in, &args); err != nil {
		return nil, err
	}
	if c.run.ctx.DispatchID == "" || c.run.ctx.Review != "" {
		return nil, fmt.Errorf("%w: this run carries out no dispatch, so there is no worktree to commit in", errInvalidInput)
	}
	switch msg := strings.TrimSpace(args.Message); {
	case msg == "":
		return nil, fmt.Errorf("%w: message is required", errInvalidInput)
	case len(args.Message) > maxCommitMessage || !utf8.ValidString(args.Message) || strings.ContainsRune(args.Message, 0):
		return nil, fmt.Errorf("%w: message must be text of at most %d bytes", errInvalidInput, maxCommitMessage)
	case len(args.Paths) > maxCommitPaths:
		return nil, fmt.Errorf("%w: at most %d paths", errInvalidInput, maxCommitPaths)
	}
	if err := c.jobRunnable(); err != nil {
		return nil, err
	}
	c.runs.mu.Lock()
	sm := c.runs.sites
	c.runs.mu.Unlock()
	if sm == nil || c.run.workdir == "" {
		return nil, fmt.Errorf("%w: this run has no Site worktree to commit in", errInvalidInput)
	}
	got, err := sm.CommitWorktree(c.run.ctx.Job, c.run.workdir, strings.TrimSpace(args.Message), args.Paths, c.run.gitEnv)
	if err != nil {
		return nil, err
	}
	if !got.Committed {
		return Committed{Status: "nothing_to_commit", Branch: got.Branch, SHA: got.SHA}, nil
	}
	return Committed{Status: "committed", Branch: got.Branch, SHA: got.SHA, Files: got.Files}, nil
}
