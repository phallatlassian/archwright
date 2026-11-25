package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phallatlassian/archwright/internal/runner"
)

// restarted is a new adapter over the same server and files: a Works that was
// stopped and started again. The Sessions it launched are still in their panes.
func (r *tmuxRig) restarted() *Tmux {
	r.tm.Close()
	return &Tmux{
		Tmux: r.tm.Tmux, Socket: r.tm.Socket, HookCommand: r.tm.HookCommand, Dir: r.tm.Dir,
		SocketDir: r.tm.SocketDir, UserConf: "-", StartTimeout: r.tm.StartTimeout, CancelGrace: r.tm.CancelGrace,
		Poll: r.tm.Poll, NudgeTimeout: r.tm.NudgeTimeout, NudgePasteDelay: r.tm.NudgePasteDelay,
		Claude: r.tm.Claude, CodexBin: r.tm.CodexBin, UserCodexHome: r.tm.UserCodexHome,
	}
}

func TestTmuxAdoptTakesOverALivePaneAndFollowsItsHooksFromTheSpool(t *testing.T) {
	for _, vendor := range []string{"claude", "codex"} {
		t.Run(vendor, func(t *testing.T) {
			r := newTmuxRig(t, vendor)
			req := r.request(t, scenTwoPrompts)
			first := r.launch(t, req)
			waitFor(t, "a turn", func() bool { return first.Info().Turns == 1 })
			was := first.Info()

			tm2 := r.restarted()
			t.Cleanup(tm2.Close)
			var ad Adopter = tm2
			if vendor == "codex" {
				ad = tm2.Codex().(Adopter)
			}
			s, err := ad.Adopt(was.Pane, "an-earlier-run", req.Run)
			if err != nil {
				t.Fatalf("adopt: %v", err)
			}
			got := s.Info()
			if got.Pane != was.Pane || got.SessionID != was.SessionID || got.Turns != 1 || got.Prompts != 1 || got.Dir != req.Dir || got.Runner != vendor {
				t.Fatalf("adopted %+v, was %+v", got, was)
			}
			if s.State() != StateIdle {
				t.Fatalf("state = %s, want the idle the spool says", s.State())
			}
			// Nothing of its history is replayed as news.
			select {
			case e := <-s.Events():
				t.Fatalf("adopted Session replayed %+v", e)
			case <-time.After(100 * time.Millisecond):
			}
			// The old Works' receiver is gone: what the Session does now reaches the new one through the spool.
			if err := s.Nudge("the new Works speaking"); err != nil {
				t.Fatalf("nudge: %v", err)
			}
			waitFor(t, "the nudged turn", func() bool { return s.Info().Turns == 2 })
			if n := s.Info().Humans; n != 0 {
				t.Fatalf("the Works' own Nudge counted as a person's: %d", n)
			}
			// A person typing into the adopted pane is recorded, not blocked.
			r.human(t, s, "and run the linter")
			waitFor(t, "the typed turn", func() bool { return s.Info().Turns == 3 })
			if n := s.Info().Humans; n != 1 {
				t.Fatalf("humans = %d", n)
			}
			s.Cancel()
			if res := s.Result(); res.Status != runner.StatusCancelled {
				t.Fatalf("result = %+v", res)
			}
			if r.exists(was.Pane) {
				t.Fatal("the pane survived cancelling the adopted Session")
			}
		})
	}
}

func (r *tmuxRig) exists(pane string) bool { return r.harness().pane.exists(pane) }

func (r *tmuxRig) human(t *testing.T, s Session, text string) { r.harness().human(t, s, text) }

func TestTmuxAdoptRefusesWhatIsNotTheSessionsLivePane(t *testing.T) {
	r := newTmuxRig(t, "claude")
	req := r.request(t, scenTurn)
	s := r.launch(t, req)
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	pane := s.Info().Pane
	tm2 := r.restarted()
	t.Cleanup(tm2.Close)

	for name, c := range map[string]struct {
		pane string
		runs []string
	}{
		"a pane that never was": {"%999", []string{req.Run}},
		"another run's pane":    {pane, []string{"run-someone-else"}},
		"no runs named":         {pane, nil},
	} {
		if got, err := tm2.Adopt(c.pane, c.runs...); !errors.Is(err, ErrNoPane) || got != nil {
			t.Errorf("%s: adopted %v, %v", name, got, err)
		}
	}
	// Its process ends and the dead pane is kept: nothing live to take over.
	s.Cancel()
	s.Result()
	if got, err := tm2.Adopt(pane, req.Run); !errors.Is(err, ErrNoPane) || got != nil {
		t.Errorf("a gone pane: adopted %v, %v", got, err)
	}
}

func TestTmuxAdoptRefusesAPaneWhoseServerIsGone(t *testing.T) {
	r := newTmuxRig(t, "claude")
	req := r.request(t, scenTurn)
	s := r.launch(t, req)
	waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	pane := s.Info().Pane
	tm2 := r.restarted()
	t.Cleanup(tm2.Close)
	r.tm.tmux("kill-server") // a reboot: the panes are lost
	if got, err := tm2.Adopt(pane, req.Run); !errors.Is(err, ErrNoPane) || got != nil {
		t.Fatalf("adopted %v, %v", got, err)
	}
}

func TestTmuxFlagsLivePanesNoRunAccountsForAndKillsNone(t *testing.T) {
	r := newTmuxRig(t, "claude")
	mine := r.launch(t, r.request(t, scenTurn))
	stray := r.launch(t, r.request(t, scenTurn))
	waitFor(t, "both turns", func() bool { return mine.Info().Turns == 1 && stray.Info().Turns == 1 })

	got, err := r.tm.FlagOrphans(map[string]bool{mine.Info().Pane: true})
	if err != nil || len(got) != 1 || got[0].Pane != stray.Info().Pane || got[0].Role != "wright" {
		t.Fatalf("orphans = %+v, %v", got, err)
	}
	if out, _ := r.tm.tmux("show-options", "-p", "-v", "-t", stray.Info().Pane, "@orphan"); out != "1" {
		t.Errorf("@orphan = %q", out)
	}
	if again, err := r.tm.FlagOrphans(map[string]bool{mine.Info().Pane: true}); err != nil || len(again) != 0 {
		t.Errorf("flagged again: %+v, %v", again, err)
	}
	if !r.exists(stray.Info().Pane) || stray.State() == StateEnded {
		t.Error("an orphan was killed")
	}
}

func TestTmuxResumeLaunchesWithTheVendorsResumeFlagInsteadOfANewSessionId(t *testing.T) {
	for _, vendor := range []string{"claude", "codex"} {
		t.Run(vendor, func(t *testing.T) {
			r := newTmuxRig(t, vendor)
			req := r.request(t, scenTurn)
			req.Resume, req.Brief = "earlier-session-id", "carry on"
			s := r.launch(t, req)
			waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
			args := r.saw(t, "args")
			if !strings.Contains(args, "earlier-session-id\n") || strings.Contains(args, "--session-id") || !strings.Contains(args, "\n--\ncarry on\n") {
				t.Fatalf("args:\n%s", args)
			}
			if vendor == "claude" && !strings.Contains(args, "--resume\nearlier-session-id\n") {
				t.Errorf("claude was not told --resume:\n%s", args)
			}
			if vendor == "codex" && !strings.HasPrefix(args, "resume\n") {
				t.Errorf("codex was not run as `codex resume`:\n%s", args)
			}
		})
	}
}
