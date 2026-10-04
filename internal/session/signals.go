package session

import (
	"context"

	"elbot/internal/signal"
)

type ChangeReason string

const (
	ChangeCreate  ChangeReason = "create"
	ChangeResume  ChangeReason = "resume"
	ChangeFork    ChangeReason = "fork"
	ChangeReset   ChangeReason = "reset"
	ChangeDelete  ChangeReason = "delete"
	ChangeExpire  ChangeReason = "expire"
	ChangeCleanup ChangeReason = "cleanup"
	ChangeMissing ChangeReason = "missing"
)

type BindingChangedEvent struct {
	Old    *Binding
	New    *Binding
	Reason ChangeReason
}

func (s *Service) BindingChanged() *signal.Signal[BindingChangedEvent] { return s.changed }

func (s *Service) setCurrent(ctx context.Context, scope Scope, id string, reason ChangeReason) {
	s.mu.Lock()
	old := s.current[scope.Key()]
	if (old == nil && id == "") || (old != nil && old.SessionID() == id) {
		s.mu.Unlock()
		return
	}
	var next *Binding
	if old != nil {
		old.valid.Store(false)
	}
	if id == "" {
		delete(s.current, scope.Key())
	} else {
		next = &Binding{scope: scope, sessionID: id}
		next.valid.Store(true)
		s.current[scope.Key()] = next
	}
	s.mu.Unlock()
	s.publishChange(ctx, BindingChangedEvent{Old: old, New: next, Reason: reason})
}

func (s *Service) clearBinding(ctx context.Context, binding *Binding, reason ChangeReason) {
	s.mu.Lock()
	if s.current[binding.Scope().Key()] != binding {
		s.mu.Unlock()
		return
	}
	binding.valid.Store(false)
	delete(s.current, binding.Scope().Key())
	s.mu.Unlock()
	s.publishChange(ctx, BindingChangedEvent{Old: binding, Reason: reason})
}

func (s *Service) clearSessionBindings(ctx context.Context, id string, reason ChangeReason) {
	s.mu.Lock()
	var removed []*Binding
	for key, b := range s.current {
		if b.SessionID() == id {
			b.valid.Store(false)
			delete(s.current, key)
			removed = append(removed, b)
		}
	}
	s.mu.Unlock()
	for _, b := range removed {
		s.publishChange(ctx, BindingChangedEvent{Old: b, Reason: reason})
	}
}
