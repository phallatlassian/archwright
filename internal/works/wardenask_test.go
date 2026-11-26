package works

import (
	"strings"
	"testing"
	"time"

	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

// wardenAsks is a Warden Session that raises a blocking Ask and ends its turn.
func wardenAsks(*store.Store) hosted.Script { return hosted.Turn(callsTool("ask", blockingAsk)) }

// askedStep starts a step whose Warden raises blockingAsk and returns the ask
// once the Warden's Session is idle with it open.
func (g *standingRig) askedStep(step string) (store.Ask, hosted.Session) {
	g.t.Helper()
	g.planFor(g.job.ID, step+":masonry")
	g.startStep(g.job.ID, step)
	var ask store.Ask
	eventually(g.t, "the Warden's ask", func() bool {
		asks, _ := g.st.Asks(store.AskFilter{Job: g.job.ID})
		if len(asks) == 1 {
			ask = asks[0]
		}
		return len(asks) == 1
	})
	sess := g.fake.Sessions()[0]
	waitSessionIdle(g.t, sess)
	return ask, sess
}

func TestAWardensBlockingAskParksItsStepAndKeepsItsSessionOpen(t *testing.T) {
	g := newStandingRigWith(t, wardenAsks)
	ask, sess := g.askedStep("one")

	sr, _ := g.st.GetStepRun(g.job.ID, "one")
	if sr.Status != store.StepBlocked {
		t.Fatalf("step is %s, want blocked", sr.Status)
	}
	if ask.Asker != RoleWarden || ask.Holder != store.HolderArchwright || ask.Step != "one" || !ask.Blocking {
		t.Fatalf("ask %+v", ask)
	}
	// The run ended with the Session idle, and nothing fails the step, nudges the Session or closes it.
	time.Sleep(150 * time.Millisecond)
	if sr, _ = g.st.GetStepRun(g.job.ID, "one"); sr.Status != store.StepBlocked {
		t.Fatalf("step is %s after its run ended, want blocked", sr.Status)
	}
	if n := len(g.fake.Nudges()); n != 0 || sess.State() != hosted.StateIdle {
		t.Fatalf("%d Nudges and Session %s with the Ask open", n, sess.State())
	}
	// The step cannot be started again, or handed back, while parked.
	if resp := call(g.t, g.arch, "again", "start_step", `{"job":"`+g.job.ID+`","step":"one"}`); resp.OK {
		t.Fatal("a parked step was started again")
	}
}

func TestAnAnswerIsNudgedIntoTheWardensOpenSessionAndTheStepResumes(t *testing.T) {
	g := newStandingRigWith(t, wardenAsks)
	ask, sess := g.askedStep("one")

	if _, err := g.r.AnswerAsk(ask.ID, RoleArchwright, AnsweredByDashboard, []store.Answer{{QuestionID: "scheme", Values: []string{"jwt"}}, {QuestionID: "note", Values: []string{"use RS256"}}}); err != nil {
		t.Fatal(err)
	}
	sr := g.handedBack(g.job.ID, "one")
	if sr.Status != store.StepHandedBack {
		t.Fatalf("step %+v", sr)
	}
	if n := len(g.fake.Launches()); n != 1 || len(g.fake.Sessions()) != 1 {
		t.Fatalf("%d Sessions launched for an answer, want the same one", n)
	}
	nudges := g.fake.Nudges()
	if len(nudges) != 1 {
		t.Fatalf("%d Nudges, want 1", len(nudges))
	}
	for _, want := range []string{"answered", "## Questions and answers", "Which auth scheme?", "A: jwt", "A: use RS256", "export ARCHWRIGHT_RUN=run-"} {
		if !strings.Contains(nudges[0], want) {
			t.Errorf("the Nudge lacks %q:\n%s", want, nudges[0])
		}
	}
	if strings.Contains(nudges[0], "standing Warden") || strings.Contains(nudges[0], "Do one") {
		t.Errorf("the Nudge repeats the step brief or calls the answer a new step:\n%s", nudges[0])
	}
	if sess.State() == hosted.StateEnded {
		t.Error("the Session ended")
	}
	if got, _ := g.st.GetAsk(ask.ID); got.Status != store.AskAnswered {
		t.Errorf("ask %+v", got)
	}
}

func TestAnAnswerStartsAFreshWardenRunWithTheStepBriefAndTheQAWhenTheSessionIsGone(t *testing.T) {
	g := newStandingRigWith(t, wardenAsks)
	ask, old := g.askedStep("one")
	old.Cancel() // the pane is gone while the Ask waits
	old.Result()
	g.fake.SetScript(hosted.Turn(carriesOutItsStep(g.st)))

	if _, err := g.r.AnswerAsk(ask.ID, RoleArchwright, AnsweredByDashboard, []store.Answer{{QuestionID: "scheme", Values: []string{"jwt"}}, {QuestionID: "note", Values: []string{"use RS256"}}}); err != nil {
		t.Fatal(err)
	}
	g.handedBack(g.job.ID, "one")
	launches := g.fake.Launches()
	if len(launches) != 2 || len(g.fake.Nudges()) != 0 {
		t.Fatalf("%d launches and %d Nudges, want a fresh Session and no Nudge", len(launches), len(g.fake.Nudges()))
	}
	for _, want := range []string{"Do one", "## Questions and answers", "Which auth scheme?", "A: jwt", "A: use RS256"} {
		if !strings.Contains(launches[1].Brief, want) {
			t.Errorf("the fresh Session's brief lacks %q:\n%s", want, launches[1].Brief)
		}
	}
}

func TestCancellingAWardensAskFailsItsParkedStep(t *testing.T) {
	g := newStandingRigWith(t, wardenAsks)
	ask, _ := g.askedStep("one")
	if _, err := g.r.CancelAsk(ask.ID, AnsweredByDashboard); err != nil {
		t.Fatal(err)
	}
	if sr, _ := g.st.GetStepRun(g.job.ID, "one"); sr.Status != store.StepFailed {
		t.Fatalf("step %+v", sr)
	}
}
