package works

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	hosted "github.com/phallatlassian/archwright/internal/session"
	"github.com/phallatlassian/archwright/internal/store"
)

func attachItems(t *testing.T, st *store.Store) []store.NeedsYouItem {
	t.Helper()
	all, err := st.NeedsYou()
	if err != nil {
		t.Fatal(err)
	}
	var out []store.NeedsYouItem
	for _, it := range all {
		if it.Kind == store.NeedsInteractiveWright {
			out = append(out, it)
		}
	}
	return out
}

func TestInteractiveWrightFilesAnAttachItemUntilItReports(t *testing.T) {
	release := make(chan struct{})
	report := callsTool("report_result", `{"status":"done","result":"verdict: A","evidence":["tried it"]}`)
	g := newSessionRig(t, hosted.Turn(func(ctx context.Context, req hosted.LaunchRequest) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return report(ctx, req)
	}))
	g.ok(g.warden, "dispatch_wright", `{"piece":"p","interactive":true,"brief":`+goodBrief+`}`, nil)

	var items []store.NeedsYouItem
	eventually(t, "the attach item", func() bool { items = attachItems(t, g.st); return len(items) == 1 })
	it := items[0]
	var detail map[string]string
	json.Unmarshal(it.Detail, &detail)
	if it.JobID != g.job.ID || detail["role"] != RoleWright || detail["piece"] != "p" || detail["pane"] == "" {
		t.Fatalf("item = %+v %v", it, detail)
	}
	for _, want := range []string{"tmux -L archwright attach-session -t =factory:" + g.job.ID, "select-pane -t " + detail["pane"]} {
		if !strings.Contains(detail["attach"], want) || !strings.Contains(it.Summary, want) {
			t.Fatalf("attach command lacks %q: %q / %q", want, detail["attach"], it.Summary)
		}
	}
	inbox, _ := g.st.Inbox(false)
	reported := 0
	for _, i := range inbox {
		if i.Name == "interactive_wright" {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("%d reports to the Archwright", reported)
	}

	close(release)
	g.waitReported("p")
	eventually(t, "the item to clear", func() bool { return len(attachItems(t, g.st)) == 0 })
}

func TestOrdinaryWrightFilesNoAttachItem(t *testing.T) {
	g := newSessionRig(t, hosted.Turn(callsTool("report_result", `{"status":"done","result":"x","evidence":["e"]}`)))
	g.send("p")
	g.waitReported("p")
	if n := len(attachItems(t, g.st)); n != 0 {
		t.Fatalf("%d attach items", n)
	}
}

func TestAttachItemIsNotRepeatedForTheSameWright(t *testing.T) {
	st := newGateRig(t, store.Gates{}).st
	announce := AnnounceInteractive(st)
	req := RunRequest{Job: "j1", Piece: "p", Guild: "masonry", Interactive: true}
	announce(req, hosted.Info{Pane: "%1"})
	announce(req, hosted.Info{Pane: "%1"})
	if n := len(attachItems(t, st)); n != 1 {
		t.Fatalf("%d attach items", n)
	}
}
