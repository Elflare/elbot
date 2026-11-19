package storage

import (
	"fmt"

	"elbot/internal/operationresult"
	"slices"
	"strings"
	"sync"
)

// Question stays with its originating Session until its answer is submitted.
type Question struct {
	ID        int64          `json:"id"` // Process-local question number.
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
	next     int64
	sessions map[string][]Question
	claims   map[int64]*AnswerBatch
}

type AnswerBatch struct {
	set       *QuestionSet
	session   string
	IDs       []int64
	handedOff bool            // Input preparation has taken the final draft snapshot.
	done      <-chan struct{} // Execution lifetime; an uncommitted batch becomes editable/retryable on stop.
}

func (s *QuestionSet) Create(items []Question) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string][]Question)
		s.claims = make(map[int64]*AnswerBatch)
	}
	for i := range items {
		s.next++
		items[i].ID = s.next
		id := items[i].SessionID
		s.sessions[id] = append(s.sessions[id], items[i])
	}
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
		if b := s.claims[id]; b != nil && b.handedOff && b.active() {
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
		if q.Draft == nil {
			continue
		}
		if old := s.claims[q.ID]; old != nil && old.active() {
			continue
		}
		s.claims[q.ID] = b
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
	return s.claims[b.IDs[0]] == b
}
func (b *AnswerBatch) Text() string {
	s := b.set
	s.mu.Lock()
	defer s.mu.Unlock()
	b.handedOff = true
	var parts []string
	for _, q := range s.sessions[b.session] {
		if s.claims[q.ID] != b {
			continue
		}
		d := q.Draft
		ans := "答：" + d.Text
		if d.Choice != nil {
			ans = fmt.Sprintf("选：%d. %s", *d.Choice, q.Options[*d.Choice-1])
			if d.Text != "" {
				ans += "\n补：" + d.Text
			}
		}
		parts = append(parts, fmt.Sprintf("#%d %s\n%s", q.ID, q.Question, ans))
	}
	return strings.Join(parts, "\n\n")
}
func (b *AnswerBatch) Finish(sent bool) {
	s := b.set
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[b.session] = slices.DeleteFunc(s.sessions[b.session], func(q Question) bool {
		if s.claims[q.ID] != b {
			return false
		}
		delete(s.claims, q.ID)
		return sent
	})
	if len(s.sessions[b.session]) == 0 {
		delete(s.sessions, b.session)
	}
}
