package session

import (
	"strings"
	"testing"
)

func TestPlanGridOrdersGuildsAndPadsEveryColumnToTheSameRows(t *testing.T) {
	cols := planGrid([]gridPane{
		{ID: "%1", Guild: "survey", Role: "warden"},
		{ID: "%2", Guild: "masonry", Role: "wright"},
		{ID: "%3", Guild: "masonry", Role: "warden"},
		{ID: "%4", Guild: "masonry", Role: "wright"},
		{ID: "%5", Guild: "zeta", Role: "warden"},
		{ID: "%6", Guild: "chancery", Role: "warden"},
		{ID: "%9", Guild: "survey", Role: "wright", Placeholder: true}, // not a Session: its slot is recomputed
	})
	var got []string
	for _, c := range cols {
		var ids []string
		for _, s := range c {
			ids = append(ids, s.ID+"/"+s.Role)
		}
		got = append(got, strings.Join(ids, ","))
	}
	want := []string{
		"%3/warden,%2/wright,%4/wright", // masonry
		"%6/warden,/wright,/wright",     // chancery
		"%1/warden,/wright,/wright",     // survey
		"%5/warden,/wright,/wright",     // an unnamed Guild follows the named ones
	}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("grid =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPlanGridGivesAGuildWithoutAWardenAnEmptyTopSlot(t *testing.T) {
	cols := planGrid([]gridPane{{ID: "%1", Guild: "masonry", Role: "wright"}})
	if len(cols) != 1 || len(cols[0]) != 2 || cols[0][0].ID != "" || cols[0][0].Role != "warden" || cols[0][1].ID != "%1" {
		t.Fatalf("grid = %+v", cols)
	}
}

func TestGridLayoutDividesTheWindowAndChecksum(t *testing.T) {
	cols := [][]slot{{{ID: "%1"}, {ID: "%2"}}, {{ID: "%3"}, {ID: "%4"}}}
	got := gridLayout(cols, 201, 51)
	const body = "201x51,0,0{100x51,0,0[100x25,0,0,1,100x25,0,26,2],100x51,101,0[100x25,101,0,3,100x25,101,26,4]}"
	if !strings.HasSuffix(got, ","+body) || len(got) != len(body)+5 {
		t.Fatalf("layout = %q, want body %q", got, body)
	}
}

// The grid is built in a real window and the layout string must be one tmux accepts.
func TestTmuxLaysAJobsWindowOutAsAColumnPerGuildWithPlaceholders(t *testing.T) {
	r := newTmuxRig(t, "claude")
	launch := func(guild, role string) Session {
		req := r.request(t, scenTurn)
		req.Job, req.Guild, req.Role = "job-g", guild, role
		return r.launch(t, req)
	}
	hallmark := launch("hallmark", "wright")
	masonry := launch("masonry", "warden")
	for _, s := range []Session{hallmark, masonry} {
		waitFor(t, "a turn", func() bool { return s.Info().Turns == 1 })
	}
	panes := func() []string {
		out, err := r.tm.tmux("list-panes", "-t", "factory:job-g", "-F", "#{pane_left} #{pane_top} #{@guild} #{@role} #{?@run,run,empty} #{@state}")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(out, "\n")
	}
	// masonry column: its warden, an empty wright; hallmark column: an empty warden, its wright.
	waitFor(t, "state labels", func() bool { return strings.Contains(strings.Join(panes(), "\n"), "idle") })
	got := strings.Join(panes(), "\n")
	for _, want := range []string{" masonry warden run ", " masonry wright empty ", " hallmark warden empty ", " hallmark wright run "} {
		if !strings.Contains(got, want) {
			t.Fatalf("panes lack %q:\n%s", want, got)
		}
	}
	rows := panes()
	if len(rows) != 4 {
		t.Fatalf("want 4 panes, got\n%s", got)
	}
	// column-major order: masonry's two panes share a left edge to the left of hallmark's.
	if left := func(i int) string { return strings.Fields(rows[i])[0] }; left(0) != left(1) || left(2) != left(3) || left(0) == left(2) {
		t.Fatalf("not one column per Guild:\n%s", got)
	}
	if !strings.Contains(rows[0], "masonry warden") || !strings.Contains(rows[2], "hallmark warden") {
		t.Fatalf("columns out of order, or warden not on top:\n%s", got)
	}

	// A second wright in masonry takes the empty slot: no new pane.
	w2 := launch("masonry", "wright")
	waitFor(t, "a turn", func() bool { return w2.Info().Turns == 1 })
	rows = panes()
	if len(rows) != 4 || strings.Contains(strings.Join(rows, "\n"), "masonry wright empty") {
		t.Fatalf("the empty slot was not taken:\n%s", strings.Join(rows, "\n"))
	}
	// A third makes a row for every column: hallmark gains an empty one.
	w3 := launch("masonry", "wright")
	waitFor(t, "a turn", func() bool { return w3.Info().Turns == 1 })
	rows = panes()
	if len(rows) != 6 || strings.Count(strings.Join(rows, "\n"), "hallmark wright empty") != 1 {
		t.Fatalf("want 2 columns x 3 rows with one empty hallmark wright:\n%s", strings.Join(rows, "\n"))
	}
}

func TestTmuxBorderLabelFollowsThePaneOptionsAndStateTransitions(t *testing.T) {
	r := newTmuxRig(t, "claude")
	req := r.request(t, scenPermHold)
	req.Job, req.Guild, req.Role = "job-l", "masonry", "wright"
	s := r.launch(t, req)
	waitFor(t, "permission", func() bool { return s.State() == StateWaitingOnPermission })
	label := func() string {
		out, err := r.tm.tmux("display-message", "-p", "-t", s.Info().Pane, "#{T:pane-border-format}")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	waitFor(t, "the permission mark", func() bool { return strings.Contains(label(), "masonry · wright · "+req.Run+" · ⚠ permission") })
}

func TestTmuxFactoryConfigSetsTheFactoryLook(t *testing.T) {
	r := newTmuxRig(t, "claude")
	req := r.request(t, scenTurn)
	req.Guild = "masonry"
	r.launch(t, req)
	for opt, want := range map[string]string{"pane-border-status": "top", "status-right": `''`, "window-status-format": `"[#I] #W"`} {
		got, err := r.tm.tmux("show-options", "-gw", opt)
		if err != nil {
			got, err = r.tm.tmux("show-options", "-g", opt)
		}
		if err != nil || !strings.HasSuffix(got, " "+want) {
			t.Errorf("%s = %q (%v), want ...%s", opt, got, err, want)
		}
	}
	if got, _ := r.tm.tmux("show-options", "-gw", "pane-border-lines"); !strings.HasSuffix(got, " double") {
		t.Errorf("pane-border-lines = %q", got)
	}
}

func TestTmuxArchwrightPaneShowsItsTitleInPurple(t *testing.T) {
	r := newTmuxRig(t, "claude")
	req := r.request(t, scenTurn)
	req.Role = RoleArchwright
	s := r.launch(t, req)
	pane := s.Info().Pane
	r.tm.tmux("select-pane", "-t", pane, "-T", "the-archwright")
	if got, _ := r.tm.tmux("display-message", "-p", "-t", pane, "#{T:pane-border-format}"); !strings.Contains(got, "the-archwright") || strings.Contains(got, " · ") {
		t.Errorf("label = %q, want the pane title", got)
	}
	if got, _ := r.tm.tmux("display-message", "-p", "-t", pane, "#{T:pane-border-style}"); !strings.Contains(got, "fg=colour141") {
		t.Errorf("border style = %q, want purple", got)
	}
}
