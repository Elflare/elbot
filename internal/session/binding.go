package session

import (
	"context"
	"errors"
	"sync/atomic"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

// Binding identifies one activation, not the lifetime of the persisted row.
// It is created and invalidated only by Service.
type Binding struct {
	scope     Scope
	sessionID string
	valid     atomic.Bool
}

func (b *Binding) Scope() Scope      { return b.scope }
func (b *Binding) SessionID() string { return b.sessionID }
func (b *Binding) Valid() bool       { return b != nil && b.valid.Load() }

type bindingContextKey struct{}

func WithBinding(ctx context.Context, binding *Binding) context.Context {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = ""
	if binding != nil {
		facts.SessionID = binding.sessionID
	}
	ctx = contextinfo.WithExecution(ctx, facts)
	return context.WithValue(ctx, bindingContextKey{}, binding)
}

func BindingFromContext(ctx context.Context) (*Binding, bool) {
	b, ok := ctx.Value(bindingContextKey{}).(*Binding)
	return b, ok && b != nil
}

// CurrentBound returns the persisted snapshot and its original activation
// together, so a later switch-away-and-back cannot rebind an old call.
func (s *Service) CurrentBound(ctx context.Context, scope Scope) (*storage.Session, *Binding, error) {
	ctx, release, err := s.EnterScope(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	s.mu.Lock()
	b := s.current[scope.Key()]
	s.mu.Unlock()
	if b == nil {
		return nil, nil, storage.ErrNotFound
	}
	row, err := s.store.Sessions().Get(ctx, b.SessionID())
	if errors.Is(err, storage.ErrNotFound) {
		s.clearBinding(ctx, b, ChangeMissing)
	}
	if err == nil && !b.Valid() {
		return nil, b, storage.ErrNotFound
	}
	return row, b, err
}
