package storage

import (
	"context"
	"fmt"

	"elbot/internal/operationresult"
	"slices"
	"strings"
	"sync"
)

// Question stays with its originating Session until its answer is submitted.
type Question struct {
	group     *QuestionGroup // Owning call; not part of the public question representation.
	ID        int64          `json:"id"` // One-based number within this Session.
	SessionID string         `json:"session_id"`
	Question  string         `json:"question"`
	Options   []string       `json:"options"`         // In the one-based order displayed to the user.
	Draft     *QuestionDraft `json:"draft,omitempty"` // Nil until the user saves an answer.
}

type QuestionDraft struct {
	Choice *int   `json:"choice,omitempty"` // One-based option, omitted for a free-text answer.
	Text   string `json:"text,omitempty"`   // Supplement to a choice or the whole free-text answer.
}

// QuestionSet is process-local. Pending entries refer to live drafts, not copies.
type QuestionSet struct {
	mu       sync.Mutex
	next     map[string]int64
	sessions map[string][]Question
	claims   map[questionKey]*AnswerBatch
}

type questionKey struct {
	session string
	id      int64
}

type AnswerBatch struct {
	set       *QuestionSet
	session   string
	IDs       []int64
	handedOff bool            // Input preparation has taken the final draft snapshot.
	done      <-chan struct{} // Execution lifetime; an uncommitted batch becomes editable/retryable on stop.
}

// QuestionGroup binds one displayed batch to its caller. A nil done channel means
// answers use normal deferred session input instead of waking a waiting tool.
type QuestionGroup struct {
	set     *QuestionSet
	session string
	done    chan struct{}
	answers map[int64]string
	ids     []int64
}

func (g *QuestionGroup) Cancel() {
	g.set.mu.Lock()
	defer g.set.mu.Unlock()
	g.set.sessions[g.session] = slices.DeleteFunc(g.set.sessions[g.session], func(q Question) bool {
		if q.group != g {
			return false
		}
		delete(g.set.claims, questionKey{g.session, q.ID})
		return true
	})
}
func (g *QuestionGroup) Result(ctx context.Context, receipt string) (string, error) {
	if g.done == nil {
		return receipt, nil
	}
	select {
	case <-ctx.Done():
		g.Cancel()
		return "", ctx.Err()
	case <-g.done:
	}
	parts := make([]string, 0, len(g.ids))
	for _, id := range g.ids {
		parts = append(parts, g.answers[id])
	}
	return strings.Join(parts, "\n\n"), nil
}

func (s *QuestionSet) Create(items []Question, async bool) *QuestionGroup {
	group := &QuestionGroup{set: s, session: items[0].SessionID}
	if !async {
		group.done = make(chan struct{})
		group.answers = map[int64]string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string][]Question)
		s.next = make(map[string]int64)
		s.claims = make(map[questionKey]*AnswerBatch)
	}
	for i := range items {
		id := items[i].SessionID
		s.next[id]++
		items[i].ID = s.next[id]
		items[i].group = group
		if !async {
			group.ids = append(group.ids, items[i].ID)
		}
		s.sessions[id] = append(s.sessions[id], items[i])
	}
	return group
}
func (s *QuestionSet) List(sid string) []Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Question{}, s.sessions[sid]...)
}
func (s *QuestionSet) Save(sid string, id int64, d QuestionDraft) operationresult.Result[Question] {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sessions[sid] {
		q := &s.sessions[sid][i]
		if q.ID != id {
			continue
		}
		if b := s.claims[questionKey{sid, id}]; b != nil && b.handedOff && b.active() {
			return operationresult.Result[Question]{Status: operationresult.Conflict, Message: "答案发送中"}
		}
		d.Text = strings.TrimSpace(d.Text)
		if d.Choice == nil && d.Text == "" {
			return operationresult.Result[Question]{Status: operationresult.InvalidArguments, Message: "缺少回答"}
		}
		if d.Choice != nil && (*d.Choice < 1 || *d.Choice > len(q.Options)) {
			return operationresult.Result[Question]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("选项：1–%d", len(q.Options))}
		}
		q.Draft = &d
		return operationresult.Result[Question]{Status: operationresult.Success, Data: *q}
	}
	return operationresult.Result[Question]{Status: operationresult.NotFound, Message: "无此待答题"}
}
func (b *AnswerBatch) active() bool {
	select {
	case <-b.done:
		return false
	default:
		return true
	}
}
func (s *QuestionSet) Claim(sid string) *AnswerBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := &AnswerBatch{set: s, session: sid}
	for _, q := range s.sessions[sid] {
		if q.Draft == nil || q.group.done != nil {
			continue
		}
		if old := s.claims[questionKey{q.SessionID, q.ID}]; old != nil && old.active() {
			continue
		}
		s.claims[questionKey{q.SessionID, q.ID}] = b
		b.IDs = append(b.IDs, q.ID)
	}
	return b
}
func (b *AnswerBatch) Bind(done <-chan struct{}) {
	b.set.mu.Lock()
	defer b.set.mu.Unlock()
	b.done = done
}
func (b *AnswerBatch) Pending() bool {
	s := b.set
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims[questionKey{b.session, b.IDs[0]}] == b
}
func (b *AnswerBatch) Text() string {
	s := b.set
	s.mu.Lock()
	defer s.mu.Unlock()
	b.handedOff = true
	var parts []string
	for _, q := range s.sessions[b.session] {
		if s.claims[questionKey{q.SessionID, q.ID}] != b {
			continue
		}
		parts = append(parts, questionAnswer(q))
	}
	return strings.Join(parts, "\n\n")
}
func questionAnswer(q Question) string {
	d := q.Draft
	ans := "答：" + d.Text
	if d.Choice != nil {
		ans = fmt.Sprintf("选：%d. %s", *d.Choice, q.Options[*d.Choice-1])
		if d.Text != "" {
			ans += "\n补：" + d.Text
		}
	}
	return fmt.Sprintf("#%d %s\n%s", q.ID, q.Question, ans)
}

// SubmitWaiting snapshots only drafts destined for a synchronous tool result.
func (s *QuestionSet) SubmitWaiting(session string) (ids []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions[session]) == 0 {
		return nil
	}
	s.sessions[session] = slices.DeleteFunc(s.sessions[session], func(q Question) bool {
		g := q.group
		if q.Draft == nil || g.done == nil {
			return false
		}
		ids = append(ids, q.ID)
		g.answers[q.ID] = questionAnswer(q)
		if len(g.answers) == len(g.ids) {
			close(g.done)
		}
		return true
	})
	return
}
func (b *AnswerBatch) Finish(sent bool) {
	s := b.set
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[b.session] = slices.DeleteFunc(s.sessions[b.session], func(q Question) bool {
		if s.claims[questionKey{q.SessionID, q.ID}] != b {
			return false
		}
		delete(s.claims, questionKey{q.SessionID, q.ID})
		return sent
	})
	if len(s.sessions[b.session]) == 0 {
		delete(s.sessions, b.session)
	}
}
