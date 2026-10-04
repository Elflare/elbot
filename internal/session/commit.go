package session

import (
	"context"
	"fmt"
)

// EnterBinding admits a short operation against the original activation.
// File callers acquire their target lock first and release this admission
// after the final filesystem commit and backup bookkeeping.
func (s *Service) EnterBinding(ctx context.Context, binding *Binding) (context.Context, func(), error) {
	if binding == nil || !binding.Valid() {
		return ctx, nil, fmt.Errorf("session binding expired")
	}
	locked, release, err := s.EnterScope(ctx, binding.Scope())
	if err != nil {
		return ctx, nil, err
	}
	locked, releaseSession, err := s.EnterSessions(locked, binding.SessionID())
	if err != nil {
		release()
		return ctx, nil, err
	}
	leave := func() { releaseSession(); release() }
	s.mu.Lock()
	valid := binding.Valid() && s.current[binding.Scope().Key()] == binding
	s.mu.Unlock()
	if !valid {
		leave()
		return ctx, nil, fmt.Errorf("session binding expired")
	}
	if err := ctx.Err(); err != nil {
		leave()
		return ctx, nil, err
	}
	return locked, leave, nil
}
