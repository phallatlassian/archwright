package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Ask kinds (ADR 0015).
const (
	AskDecision      = "decision"
	AskManualStep    = "manual-step"
	AskClarification = "clarification"
)

// Ask statuses. An escalated ask waits on the ask its Warden raised from it.
const (
	AskOpen      = "open"
	AskEscalated = "escalated"
	AskAnswered  = "answered"
	AskCancelled = "cancelled"
)

// Who holds an ask: the layer that must answer it next.
const (
	HolderWarden     = "warden"
	HolderArchwright = "archwright"
)

// Needs-you item kind for an ask that reached the Archwright.
const NeedsAsk = "ask"

// Question is one question of an ask, with optional options and a recommended default.
type Question struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Options  []string `json:"options,omitempty"`
	Default  string   `json:"default,omitempty"` // the recommended default; one of Options if there are any
	Multiple bool     `json:"multiple,omitempty"`
}

// Answer is the reply to one question: values, or a skip (the next brief says it is unanswered).
type Answer struct {
	QuestionID string   `json:"question_id"`
	Values     []string `json:"values,omitempty"`
	Skipped    bool     `json:"skipped,omitempty"`
}

// Ask is a structured request for a human decision, kept in the Works (ADR 0015).
type Ask struct {
	ID         string     `json:"id"`
	JobID      string     `json:"job_id"`
	Asker      string     `json:"asker"` // wright or warden
	Guild      string     `json:"guild,omitempty"`
	Piece      string     `json:"piece,omitempty"`
	DispatchID string     `json:"dispatch_id,omitempty"` // the Wright dispatch that is blocked on it
	ParentID   string     `json:"parent_id,omitempty"`   // the ask this one escalates
	Kind       string     `json:"kind"`
	Blocking   bool       `json:"blocking"`
	Why        string     `json:"why,omitempty"`
	Questions  []Question `json:"questions"`
	Holder     string     `json:"holder"`
	Status     string     `json:"status"`
	Answers    []Answer   `json:"answers"`
	AnsweredBy string     `json:"answered_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
}

// NewAsk is an ask as its raiser supplies it.
type NewAsk struct {
	JobID, Asker, Guild, Piece, DispatchID, ParentID, Kind, Why string
	Blocking                                                    bool
	Questions                                                   []Question
}

func invalid(format string, a ...any) error { return fmt.Errorf("invalid input: "+format, a...) }

// normalizeQuestions numbers questions that have no id and checks the rest.
func normalizeQuestions(qs []Question) ([]Question, error) {
	if len(qs) == 0 {
		return nil, invalid("an ask needs at least one question")
	}
	out := make([]Question, len(qs))
	seen := map[string]bool{}
	for i, q := range qs {
		if strings.TrimSpace(q.Text) == "" {
			return nil, invalid("question %d has no text", i+1)
		}
		if q.ID == "" {
			q.ID = fmt.Sprintf("q%d", i+1)
		}
		if seen[q.ID] {
			return nil, invalid("duplicate question id %q", q.ID)
		}
		seen[q.ID] = true
		opts := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o) == "" || opts[o] {
				return nil, invalid("question %q has a blank or repeated option", q.ID)
			}
			opts[o] = true
		}
		if q.Default != "" && len(q.Options) > 0 && !opts[q.Default] {
			return nil, invalid("question %q: the default %q is not one of its options", q.ID, q.Default)
		}
		if q.Multiple && len(q.Options) == 0 {
			return nil, invalid("question %q: multiple needs options", q.ID)
		}
		out[i] = q
	}
	return out, nil
}

// ValidateAnswers checks answers against the questions: every question
// exactly once, each answered with its options or skipped. They come back in
// question order.
func ValidateAnswers(qs []Question, answers []Answer) ([]Answer, error) {
	byID := map[string]Answer{}
	for _, a := range answers {
		if _, dup := byID[a.QuestionID]; dup {
			return nil, invalid("question %q is answered twice", a.QuestionID)
		}
		byID[a.QuestionID] = a
	}
	out := make([]Answer, 0, len(qs))
	for _, q := range qs {
		a, ok := byID[q.ID]
		if !ok {
			return nil, invalid("question %q has no answer (skip it to leave it unanswered)", q.ID)
		}
		delete(byID, q.ID)
		if a.Skipped {
			out = append(out, Answer{QuestionID: q.ID, Skipped: true})
			continue
		}
		opts := map[string]bool{}
		for _, o := range q.Options {
			opts[o] = true
		}
		for _, v := range a.Values {
			if strings.TrimSpace(v) == "" {
				return nil, invalid("question %q has a blank answer", q.ID)
			}
			if len(opts) > 0 && !opts[v] {
				return nil, invalid("question %q: %q is not one of its options", q.ID, v)
			}
		}
		if n := len(a.Values); n == 0 || (n > 1 && !q.Multiple) {
			return nil, invalid("question %q needs exactly one answer", q.ID)
		}
		out = append(out, Answer{QuestionID: q.ID, Values: a.Values})
	}
	for id := range byID {
		return nil, invalid("no question %q", id)
	}
	return out, nil
}

// DefaultAnswers accepts every question's recommended default; it fails if one has none.
func DefaultAnswers(qs []Question) ([]Answer, error) {
	out := make([]Answer, len(qs))
	for i, q := range qs {
		if q.Default == "" {
			return nil, invalid("question %q has no recommended default to accept", q.ID)
		}
		out[i] = Answer{QuestionID: q.ID, Values: []string{q.Default}}
	}
	return out, nil
}

// SkipAnswers skips every question.
func SkipAnswers(qs []Question) []Answer {
	out := make([]Answer, len(qs))
	for i, q := range qs {
		out[i] = Answer{QuestionID: q.ID, Skipped: true}
	}
	return out
}

const askCols = "id, job_id, asker, guild, piece, dispatch_id, parent_id, kind, blocking, why, questions, holder, status, answers, answered_by, created_at, COALESCE(answered_at,'')"

func scanAsk(r scanner) (Ask, error) {
	var a Ask
	var qs, ans, created, answered string
	if err := r.Scan(&a.ID, &a.JobID, &a.Asker, &a.Guild, &a.Piece, &a.DispatchID, &a.ParentID, &a.Kind, &a.Blocking, &a.Why, &qs, &a.Holder, &a.Status, &ans, &a.AnsweredBy, &created, &answered); err != nil {
		return Ask{}, err
	}
	if err := json.Unmarshal([]byte(qs), &a.Questions); err != nil {
		return Ask{}, err
	}
	if err := json.Unmarshal([]byte(ans), &a.Answers); err != nil {
		return Ask{}, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if answered != "" {
		t, _ := time.Parse(time.RFC3339Nano, answered)
		a.AnsweredAt = &t
	}
	return a, nil
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

func getAsk(q rowQuerier, id string) (Ask, error) {
	a, err := scanAsk(q.QueryRow("SELECT "+askCols+" FROM asks WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Ask{}, fmt.Errorf("ask %q: %w", id, ErrNotFound)
	}
	return a, err
}

func listAsks(q rowQuerier, where string, args ...any) ([]Ask, error) {
	rows, err := q.Query("SELECT "+askCols+" FROM asks "+where+" ORDER BY rowid", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ask{}
	for rows.Next() {
		a, err := scanAsk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAsk reads one ask.
func (s *Store) GetAsk(id string) (Ask, error) { return getAsk(s.db, id) }

// AskFilter selects asks; an empty field matches all.
type AskFilter struct{ Job, Guild, Status, Holder string }

// Asks lists asks, oldest first.
func (s *Store) Asks(f AskFilter) ([]Ask, error) {
	var conds []string
	var args []any
	for col, v := range map[string]string{"job_id": f.Job, "guild": f.Guild, "status": f.Status, "holder": f.Holder} {
		if v != "" {
			conds = append(conds, col+" = ?")
			args = append(args, v)
		}
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	return listAsks(s.db, where, args...)
}

// PieceAsks lists the asks Wrights raised on a piece, which its briefs carry.
func (s *Store) PieceAsks(jobID, piece string) ([]Ask, error) {
	return listAsks(s.db, "WHERE job_id = ? AND piece = ? AND asker = 'wright'", jobID, piece)
}

// CreateAsk records an ask. A Wright's ask goes to its Warden, a Warden's to
// the Archwright, who also finds it in the Needs-you inbox. A blocking
// Wright ask moves its dispatch to blocked: the run ends and the piece parks.
func (s *Store) CreateAsk(n NewAsk) (Ask, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Ask{}, err
	}
	defer tx.Rollback()
	a, err := s.createAsk(tx, n)
	if err != nil {
		return Ask{}, err
	}
	return a, tx.Commit()
}

func (s *Store) createAsk(tx *sql.Tx, n NewAsk) (Ask, error) {
	switch n.Kind {
	case AskDecision, AskManualStep, AskClarification:
	default:
		return Ask{}, invalid("kind must be decision, manual-step or clarification, not %q", n.Kind)
	}
	qs, err := normalizeQuestions(n.Questions)
	if err != nil {
		return Ask{}, err
	}
	var job int
	if err := tx.QueryRow("SELECT 1 FROM jobs WHERE id = ?", n.JobID).Scan(&job); errors.Is(err, sql.ErrNoRows) {
		return Ask{}, fmt.Errorf("job %q: %w", n.JobID, ErrNotFound)
	} else if err != nil {
		return Ask{}, err
	}
	if n.DispatchID != "" && n.Blocking {
		res, err := tx.Exec("UPDATE dispatches SET status = ? WHERE id = ? AND status = ?", DispatchBlocked, n.DispatchID, DispatchRunning)
		if err != nil {
			return Ask{}, err
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return Ask{}, fmt.Errorf("dispatch %q is not running, so it cannot block on an ask: %w", n.DispatchID, ErrConflict)
		}
	}
	at := s.stamp()
	a := Ask{ID: newID("ask-"), JobID: n.JobID, Asker: n.Asker, Guild: n.Guild, Piece: n.Piece, DispatchID: n.DispatchID, ParentID: n.ParentID,
		Kind: n.Kind, Blocking: n.Blocking, Why: n.Why, Questions: qs, Holder: HolderArchwright, Status: AskOpen, Answers: []Answer{}}
	if n.Asker == "wright" {
		a.Holder = HolderWarden
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	rawQs, _ := json.Marshal(qs)
	if _, err := tx.Exec("INSERT INTO asks (id, job_id, asker, guild, piece, dispatch_id, parent_id, kind, blocking, why, questions, holder, status, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		a.ID, a.JobID, a.Asker, a.Guild, a.Piece, a.DispatchID, a.ParentID, a.Kind, a.Blocking, a.Why, string(rawQs), a.Holder, a.Status, at); err != nil {
		return Ask{}, err
	}
	if err := appendEvent(tx, at, a.JobID, "ask.raised", map[string]any{"ask": a.ID, "asker": a.Asker, "kind": a.Kind, "blocking": a.Blocking, "holder": a.Holder}); err != nil {
		return Ask{}, err
	}
	if a.Holder == HolderArchwright {
		detail, _ := json.Marshal(map[string]any{"ask_id": a.ID, "kind": a.Kind, "blocking": a.Blocking, "asker": a.Asker})
		var site string
		_ = tx.QueryRow("SELECT COALESCE(site,'') FROM jobs WHERE id = ?", a.JobID).Scan(&site)
		summary := fmt.Sprintf("%s asks: %s", a.Asker, qs[0].Text)
		if _, err := tx.Exec("INSERT OR IGNORE INTO needs_you (kind, job_id, site, summary, detail, dedupe_key, created_at) VALUES (?,?,?,?,?,?,?)",
			NeedsAsk, a.JobID, site, summary, string(detail), "ask:"+a.ID, at); err != nil {
			return Ask{}, err
		}
	}
	return a, nil
}

// EscalateAsk passes a Warden-held ask up: the Warden raises its own ask to
// the Archwright, with the same questions, and the original waits on it.
func (s *Store) EscalateAsk(id, why string) (Ask, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Ask{}, err
	}
	defer tx.Rollback()
	p, err := getAsk(tx, id)
	if err != nil {
		return Ask{}, err
	}
	if p.Status != AskOpen || p.Holder != HolderWarden {
		return Ask{}, fmt.Errorf("ask %q is %s and held by the %s, so it cannot be escalated: %w", id, p.Status, p.Holder, ErrConflict)
	}
	if _, err := tx.Exec("UPDATE asks SET status = ? WHERE id = ?", AskEscalated, id); err != nil {
		return Ask{}, err
	}
	child, err := s.createAsk(tx, NewAsk{JobID: p.JobID, Asker: "warden", Guild: p.Guild, Piece: p.Piece, ParentID: p.ID,
		Kind: p.Kind, Blocking: p.Blocking, Why: why, Questions: p.Questions})
	if err != nil {
		return Ask{}, err
	}
	return child, tx.Commit()
}

// AnswerResult is what answering an ask settled.
type AnswerResult struct {
	Resolved []Ask      `json:"resolved"` // the ask and any ask it was escalated from
	Resumed  []Dispatch `json:"resumed"`  // fresh dispatches that replace blocked ones
}

// RenderResume builds the brief of a fresh dispatch that replaces a blocked
// one: the original brief plus the Q&A record of the piece.
type RenderResume func(prev Dispatch, qa []Ask) string

// AnswerAsk records the first answer to an ask held at level (warden or
// archwright) and loses to any earlier answer, skip or cancel. The answer
// also settles the asks it was escalated from, and a blocked Wright dispatch
// is replaced by a fresh queued one carrying the Q&A record.
func (s *Store) AnswerAsk(id, level, by string, answers []Answer, render RenderResume) (AnswerResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return AnswerResult{}, err
	}
	defer tx.Rollback()
	a, err := getAsk(tx, id)
	if err != nil {
		return AnswerResult{}, err
	}
	switch {
	case a.Status == AskEscalated:
		return AnswerResult{}, fmt.Errorf("ask %q was escalated to the Archwright; answer that one: %w", id, ErrConflict)
	case a.Status != AskOpen:
		return AnswerResult{}, fmt.Errorf("ask %q is already %s (the first answer wins): %w", id, a.Status, ErrConflict)
	case a.Holder != level:
		return AnswerResult{}, fmt.Errorf("ask %q is held by the %s, not the %s: %w", id, a.Holder, level, ErrConflict)
	}
	norm, err := ValidateAnswers(a.Questions, answers)
	if err != nil {
		return AnswerResult{}, err
	}
	raw, _ := json.Marshal(norm)
	at := s.stamp()
	var res AnswerResult
	for cur := a; ; {
		if _, err := tx.Exec("UPDATE asks SET status = ?, answers = ?, answered_by = ?, answered_at = ? WHERE id = ?", AskAnswered, string(raw), by, at, cur.ID); err != nil {
			return AnswerResult{}, err
		}
		cur.Status, cur.Answers, cur.AnsweredBy = AskAnswered, norm, by
		t, _ := time.Parse(time.RFC3339Nano, at)
		cur.AnsweredAt = &t
		res.Resolved = append(res.Resolved, cur)
		if _, err := tx.Exec("DELETE FROM needs_you WHERE dedupe_key = ?", "ask:"+cur.ID); err != nil {
			return AnswerResult{}, err
		}
		if err := appendEvent(tx, at, cur.JobID, "ask.answered", map[string]any{"ask": cur.ID, "by": by}); err != nil {
			return AnswerResult{}, err
		}
		if cur.ParentID == "" {
			break
		}
		parent, err := getAsk(tx, cur.ParentID)
		if err != nil {
			return AnswerResult{}, err
		}
		if parent.Status != AskEscalated {
			break
		}
		cur = parent
	}
	for _, r := range res.Resolved {
		if r.DispatchID == "" || !r.Blocking {
			continue
		}
		prev, err := scanDispatch(tx.QueryRow("SELECT "+dispatchCols+" FROM dispatches WHERE id = ?", r.DispatchID))
		if err != nil {
			return AnswerResult{}, err
		}
		if prev.Status != DispatchBlocked {
			continue
		}
		if _, err := tx.Exec("UPDATE dispatches SET status = ? WHERE id = ?", DispatchAnswered, prev.ID); err != nil {
			return AnswerResult{}, err
		}
		qa, err := listAsks(tx, "WHERE job_id = ? AND piece = ? AND asker = 'wright' AND status = 'answered'", prev.JobID, prev.Piece)
		if err != nil {
			return AnswerResult{}, err
		}
		round, err := nextRound(tx, prev.JobID, prev.Piece)
		if err != nil {
			return AnswerResult{}, err
		}
		next, err := s.insertDispatch(tx, prev.JobID, prev.Guild, prev.Piece, round, prev.Brief, render(prev, qa))
		if err != nil {
			return AnswerResult{}, err
		}
		res.Resumed = append(res.Resumed, next)
	}
	return res, tx.Commit()
}

// CancelAsk is the developer cancelling the Job an ask belongs to: the ask,
// the asks it was escalated from and every other open ask of the Job are
// cancelled, blocked dispatches end, and the Needs-you items go. Closing the
// Job itself is the caller's. Like an answer, it loses to an earlier one.
func (s *Store) CancelAsk(id, by string) ([]Ask, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a, err := getAsk(tx, id)
	if err != nil {
		return nil, err
	}
	if a.Status != AskOpen && a.Status != AskEscalated {
		return nil, fmt.Errorf("ask %q is already %s (the first answer wins): %w", id, a.Status, ErrConflict)
	}
	live, err := listAsks(tx, "WHERE job_id = ? AND status IN ('open','escalated')", a.JobID)
	if err != nil {
		return nil, err
	}
	at := s.stamp()
	for _, l := range live {
		if _, err := tx.Exec("UPDATE asks SET status = ?, answered_by = ?, answered_at = ? WHERE id = ?", AskCancelled, by, at, l.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec("DELETE FROM needs_you WHERE dedupe_key = ?", "ask:"+l.ID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec("UPDATE dispatches SET status = ? WHERE job_id = ? AND status = ?", DispatchRejected, a.JobID, DispatchBlocked); err != nil {
		return nil, err
	}
	if err := appendEvent(tx, at, a.JobID, "ask.cancelled", map[string]any{"ask": id, "by": by}); err != nil {
		return nil, err
	}
	return live, tx.Commit()
}

// OpenAsksHeldBy lists a Job's open asks held at the given layer.
func (s *Store) OpenAsksHeldBy(jobID, holder string) ([]Ask, error) {
	return listAsks(s.db, "WHERE job_id = ? AND holder = ? AND status = 'open'", jobID, holder)
}
