package session

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// GuildOrder is the left-to-right order of the Guild columns in a Job's window.
// A Guild not named here follows, in the order its first pane appeared.
var GuildOrder = []string{"masonry", "chancery", "hallmark", "survey"}

// RoleWarden is the Role of a Warden; its pane tops its Guild's column, and
// every other Role's panes sit below it.
const RoleWarden = "warden"

// gridPane is one pane of a Job's window as the layout sees it.
type gridPane struct {
	ID          string
	Guild, Role string
	Placeholder bool // an "empty" pane that holds a slot no Session fills
}

// slot is one cell of the grid: a pane, or ID "" for a slot that needs a placeholder.
type slot struct{ ID, Guild, Role string }

// planGrid arranges a Job's window as one column per Guild, a Warden on top and
// the other Roles below. Every column has the same rows: the Guild with the
// most Wardens and the one with the most Wrights set how many, and a column
// short of either gets an empty slot (ID ""). Placeholder panes are not
// placed: the slots they would fill are what is returned.
func planGrid(panes []gridPane) [][]slot {
	var guilds []string
	wardens, wrights := map[string][]slot{}, map[string][]slot{}
	for _, p := range panes {
		if p.Placeholder {
			continue
		}
		if !slices.Contains(guilds, p.Guild) {
			guilds = append(guilds, p.Guild)
		}
		if p.Role == RoleWarden {
			wardens[p.Guild] = append(wardens[p.Guild], slot{p.ID, p.Guild, p.Role})
		} else {
			wrights[p.Guild] = append(wrights[p.Guild], slot{p.ID, p.Guild, p.Role})
		}
	}
	rank := func(g string) int {
		if i := slices.Index(GuildOrder, g); i >= 0 {
			return i
		}
		return len(GuildOrder)
	}
	slices.SortStableFunc(guilds, func(a, b string) int { return rank(a) - rank(b) })
	wardenRows, wrightRows := 1, 1
	for _, g := range guilds {
		wardenRows, wrightRows = max(wardenRows, len(wardens[g])), max(wrightRows, len(wrights[g]))
	}
	cols := make([][]slot, len(guilds))
	for i, g := range guilds {
		col := wardens[g]
		for len(col) < wardenRows {
			col = append(col, slot{"", g, RoleWarden})
		}
		col = append(col, wrights[g]...)
		for len(col) < wardenRows+wrightRows {
			col = append(col, slot{"", g, "wright"})
		}
		cols[i] = col
	}
	return cols
}

// gridLayout is the tmux layout string of the columns in a window of w by h
// cells. The panes of the grid must be in the window in column-major order:
// tmux gives a layout's cells to the window's panes in their order.
func gridLayout(cols [][]slot, w, h int) string {
	if len(cols) == 0 {
		return ""
	}
	rows := len(cols[0])
	cw := split(w-(len(cols)-1), len(cols))
	rh := split(h-(rows-1), rows)
	var nodes []string
	x := 0
	for i, col := range cols {
		var cells []string
		y := 0
		for j, s := range col {
			cells = append(cells, fmt.Sprintf("%dx%d,%d,%d,%s", cw[i], rh[j], x, y, strings.TrimPrefix(s.ID, "%")))
			y += rh[j] + 1
		}
		if rows == 1 {
			nodes = append(nodes, cells[0])
		} else {
			nodes = append(nodes, fmt.Sprintf("%dx%d,%d,0[%s]", cw[i], h, x, strings.Join(cells, ",")))
		}
		x += cw[i] + 1
	}
	var body string
	switch {
	case len(cols) == 1:
		body = nodes[0]
	default:
		body = fmt.Sprintf("%dx%d,0,0{%s}", w, h, strings.Join(nodes, ","))
	}
	return fmt.Sprintf("%04x,%s", layoutChecksum(body), body)
}

// split divides total cells into n parts, the first ones taking the remainder.
func split(total, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = total / n
		if i < total%n {
			out[i]++
		}
	}
	return out
}

// layoutChecksum is tmux's checksum of a layout string, which select-layout checks.
func layoutChecksum(s string) uint16 {
	var sum uint16
	for _, c := range []byte(s) {
		sum = sum>>1 + (sum&1)<<15
		sum += uint16(c)
	}
	return sum
}

// arrange puts a Job's window into the grid: placeholders for the empty slots
// are added or dropped, the panes are put in column-major order, and the
// layout is applied. A window holding a pane that has no Guild (a trust probe)
// is not a grid and is tiled instead. Failures are cosmetic.
func (t *Tmux) arrange(window string) {
	const format = "#{pane_id}|#{@guild}|#{@role}|#{@placeholder}"
	out, err := t.tmux("list-panes", "-t", window, "-F", format)
	if err != nil {
		return
	}
	var panes []gridPane
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "|")
		if len(f) != 4 {
			continue
		}
		if f[1] == "" && f[3] == "" {
			t.tmux("select-layout", "-t", window, "tiled")
			return
		}
		panes = append(panes, gridPane{ID: f[0], Guild: f[1], Role: f[2], Placeholder: f[3] != ""})
	}
	cols := planGrid(panes)
	var empty []*slot
	for _, c := range cols {
		for i := range c {
			if c[i].ID == "" {
				empty = append(empty, &c[i])
			}
		}
	}
	var spare []string // placeholder panes in the window
	for _, p := range panes {
		if p.Placeholder {
			spare = append(spare, p.ID)
		}
	}
	for len(spare) > len(empty) { // a Session took a slot
		t.tmux("kill-pane", "-t", spare[len(spare)-1])
		spare = spare[:len(spare)-1]
	}
	for len(spare) < len(empty) {
		id, err := t.tmux("split-window", "-d", "-t", window, "-P", "-F", "#{pane_id}", "--", "tail", "-f", "/dev/null")
		if err != nil {
			return
		}
		spare = append(spare, id)
	}
	for i, s := range empty {
		s.ID = spare[i]
		for opt, val := range map[string]string{"@guild": s.Guild, "@role": s.Role, "@placeholder": "1"} {
			t.tmux("set-option", "-p", "-t", s.ID, opt, val)
		}
	}
	var want []string
	for _, c := range cols {
		for _, s := range c {
			want = append(want, s.ID)
		}
	}
	have := t.paneOrder(window)
	for i := range want {
		if i < len(have) && have[i] != want[i] {
			if _, err := t.tmux("swap-pane", "-d", "-s", want[i], "-t", have[i]); err != nil {
				return
			}
			j := slices.Index(have, want[i])
			have[i], have[j] = have[j], have[i]
		}
	}
	size, err := t.tmux("display-message", "-p", "-t", window, "#{window_width}x#{window_height}")
	if err != nil {
		return
	}
	ws, hs, _ := strings.Cut(size, "x")
	w, _ := strconv.Atoi(ws)
	h, _ := strconv.Atoi(hs)
	if layout := gridLayout(cols, w, h); layout != "" {
		t.tmux("select-layout", "-t", window, layout)
	}
}

func (t *Tmux) paneOrder(window string) []string {
	out, err := t.tmux("list-panes", "-t", window, "-F", "#{pane_id}")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
