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
	sessions map[string][]*QuestionGroup
}

// QuestionGroup becomes visible to /a and /ac only after successful delivery.
type QuestionGroup struct {
	set       *QuestionSet
	items     []Question
	submitted chan struct{}
}

func (g *QuestionGroup) Publish() {
	g.set.mu.Lock()
	defer g.set.mu.Unlock()
	sid := g.items[0].SessionID
	g.set.sessions[sid] = append(g.set.sessions[sid], g)
}

func (g *QuestionGroup) Cancel() {
	g.set.mu.Lock()
	defer g.set.mu.Unlock()
	sid := g.items[0].SessionID
	if groups := g.set.sessions[sid]; len(groups) > 0 {
		g.set.sessions[sid] = slices.DeleteFunc(groups, func(item *QuestionGroup) bool { return item == g })
	}
}

func (g *QuestionGroup) Wait(ctx context.Context) ([]Question, error) {
	select {
	case <-g.submitted:
		return g.items, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *QuestionSet) Create(items []Question) *QuestionGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string][]*QuestionGroup)
		s.next = make(map[string]int64)
	}
	sid := items[0].SessionID
	for i := range items {
		s.next[sid]++
		items[i].ID = s.next[sid]
	}
	return &QuestionGroup{set: s, items: items, submitted: make(chan struct{})}
}
func (s *QuestionSet) List(sid string) []Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []Question{}
	for _, g := range s.sessions[sid] {
		items = append(items, g.items...)
	}
	return items
}
func (s *QuestionSet) Save(sid string, id int64, d QuestionDraft) operationresult.Result[Question] {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.sessions[sid] {
		i := id - g.items[0].ID // Each call reserves consecutive question numbers.
		if i < 0 || i >= int64(len(g.items)) {
			continue
		}
		q := &g.items[i]
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
func (s *QuestionSet) Submit(session string) (ids []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.sessions[session] {
		for _, q := range g.items {
			ids = append(ids, q.ID)
		}
		close(g.submitted)
	}
	delete(s.sessions, session)
	return ids
}
