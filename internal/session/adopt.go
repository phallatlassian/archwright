package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// sessionMeta is what the adapter keeps of a Session in its run directory, so a
// later Works can take the Session over: the token its hooks are signed with,
// which vendor it is and where it works. The pane id is the Works' to store
// (ADR 0026); this is only what the Works cannot rebuild from it.
type sessionMeta struct {
	Run    string `json:"run"`
	Token  string `json:"token"`
	Vendor string `json:"vendor"`
	Job    string `json:"job,omitempty"`
	Role   string `json:"role,omitempty"`
	Dir    string `json:"dir"`
}

const metaFile = "session.json"

func writeMeta(runDir string, m sessionMeta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, metaFile), b, 0o600)
}

func readMeta(runDir string) (sessionMeta, error) {
	var m sessionMeta
	b, err := os.ReadFile(filepath.Join(runDir, metaFile))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// Adopt implements Adopter. The pane is looked up by the id the Works
// recorded; it is the Session's only if its @run option names one of runs, and
// it is alive. The Session's state, session id and turns are rebuilt by
// replaying its hook spool, silently (they happened under the earlier Works),
// and hooks from then on are followed as for any Session.
func (t *Tmux) Adopt(pane string, runs ...string) (Session, error) {
	const format = "#{pane_dead}|#{@run}|#{window_id}"
	out, err := t.tmux("display-message", "-p", "-t", pane, format)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoPane, err)
	}
	f := strings.SplitN(out, "|", 3)
	if len(f) != 3 || f[0] == "1" || !slices.Contains(runs, f[1]) || !runIDPattern.MatchString(f[1]) {
		return nil, fmt.Errorf("%w: %s is not a live pane of one of its runs", ErrNoPane, pane)
	}
	run, window := f[1], f[2]
	runDir := filepath.Join(t.Dir, "runs", run)
	meta, err := readMeta(runDir)
	if err != nil {
		return nil, fmt.Errorf("the Session of run %s left no record to adopt it from: %w", run, err)
	}
	v, ok := vendors[meta.Vendor]
	if !ok {
		return nil, fmt.Errorf("the Session of run %s ran %q, which this Works cannot host", run, meta.Vendor)
	}
	s := &tmuxSession{t: t, v: v, core: newCore(meta.Token), pane: pane, runDir: runDir, hookFile: filepath.Join(runDir, "hooks.jsonl"), cancelCh: make(chan struct{}), started: time.Now()}
	s.core.setPane(pane, window)
	s.core.setPlace(meta.Dir, v.name)
	s.core.quiet = true
	s.drainSpool()
	s.core.catchUp()
	s.core.onState = s.markState
	s.markState(s.core.State()) // what the replay found, as the border shows it
	t.mu.Lock()
	if err := t.ensureReceiverLocked(); err != nil {
		t.mu.Unlock()
		return nil, err
	}
	unregister := t.recv.Register(run, meta.Token, s.core.deliver)
	t.mu.Unlock()
	go func() {
		defer unregister()
		s.run()
	}()
	return v.wrap(s), nil
}

// FlagOrphans implements Orphaner: a live pane that carries a Session's @run
// and is not one the Works recorded is marked with the @orphan pane option, and
// is otherwise left alone.
func (t *Tmux) FlagOrphans(known map[string]bool) ([]Orphan, error) {
	out, err := t.tmux("list-panes", "-a", "-F", "#{pane_id}|#{pane_dead}|#{@run}|#{@role}|#{@orphan}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return nil, nil // no factory, no orphans
		}
		return nil, err
	}
	var found []Orphan
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "|")
		if len(f) != 5 || f[2] == "" || f[1] == "1" || known[f[0]] || f[4] == "1" {
			continue
		}
		if _, err := t.tmux("set-option", "-p", "-t", f[0], "@orphan", "1"); err != nil {
			return found, err
		}
		found = append(found, Orphan{Pane: f[0], Run: f[2], Role: f[3]})
	}
	return found, nil
}
