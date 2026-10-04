package session

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

type scopeGate struct{ token chan struct{} }
type scopeLease struct {
	service *Service
	keys    []string
	parent  *scopeLease
	active  atomic.Bool
	mu      sync.Mutex
	events  []BindingChangedEvent
}
type scopeLeaseKey struct{}

// EnterScope serializes only one scope's short current-session operations.
func (s *Service) EnterScope(ctx context.Context, scope Scope) (context.Context, func(), error) {
	return s.enterKeys(ctx, []string{"scope:" + scope.Key()})
}

// EnterSessions coordinates execution admission and removal. Acquire a scope
// first when needed, then all affected session IDs in one sorted acquisition.
func (s *Service) EnterSessions(ctx context.Context, ids ...string) (context.Context, func(), error) {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			keys = append(keys, "session:"+id)
		}
	}
	sort.Strings(keys)
	return s.enterKeys(ctx, keys)
}

// EnterActivation includes both the current session and any target/source.
func (s *Service) EnterActivation(ctx context.Context, scope Scope, targets ...string) (context.Context, func(), error) {
	ctx, leaveScope, err := s.EnterScope(ctx, scope)
	if err != nil {
		return ctx, nil, err
	}
	s.mu.Lock()
	if b := s.current[scope.Key()]; b != nil {
		targets = append(targets, b.SessionID())
	}
	s.mu.Unlock()
	ctx, leaveSessions, err := s.EnterSessions(ctx, targets...)
	if err != nil {
		leaveScope()
		return ctx, nil, err
	}
	return ctx, func() { leaveSessions(); leaveScope() }, nil
}

func (s *Service) enterKeys(ctx context.Context, keys []string) (context.Context, func(), error) {
	parent, _ := ctx.Value(scopeLeaseKey{}).(*scopeLease)
	held := map[string]bool{}
	for p := parent; p != nil; p = p.parent {
		if p.service == s && p.active.Load() {
			for _, k := range p.keys {
				held[k] = true
			}
		}
	}
	var wanted []string
	for _, k := range keys {
		if held[k] {
			continue
		}
		for old := range held {
			if strings.HasPrefix(old, "session:") && (strings.HasPrefix(k, "scope:") || k < old) {
				return ctx, nil, fmt.Errorf("session admission lock order: %s after %s", k, old)
			}
		}
		held[k] = true
		wanted = append(wanted, k)
	}
	if len(wanted) == 0 {
		return ctx, func() {}, ctx.Err()
	}
	var gates []*scopeGate
	for _, k := range wanted {
		s.mu.Lock()
		gate := s.gates[k]
		if gate == nil {
			gate = &scopeGate{token: make(chan struct{}, 1)}
			s.gates[k] = gate
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			for _, g := range gates {
				<-g.token
			}
			return ctx, nil, ctx.Err()
		case gate.token <- struct{}{}:
			gates = append(gates, gate)
		}
	}
	if err := ctx.Err(); err != nil {
		for _, g := range gates {
			<-g.token
		}
		return ctx, nil, err
	}
	lease := &scopeLease{service: s, keys: wanted, parent: parent}
	lease.active.Store(true)
	ctx = context.WithValue(ctx, scopeLeaseKey{}, lease)
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			lease.active.Store(false)
			for i := len(gates) - 1; i >= 0; i-- {
				<-gates[i].token
			}
			lease.mu.Lock()
			events := lease.events
			lease.events = nil
			lease.mu.Unlock()
			for _, event := range events {
				s.publishChange(ctx, event)
			}
		})
	}, nil
}

func (s *Service) publishChange(ctx context.Context, event BindingChangedEvent) {
	var outer *scopeLease
	for p, _ := ctx.Value(scopeLeaseKey{}).(*scopeLease); p != nil; p = p.parent {
		if p.service == s && p.active.Load() {
			outer = p
		}
	}
	if outer != nil {
		outer.mu.Lock()
		outer.events = append(outer.events, event)
		outer.mu.Unlock()
		return
	}
	_ = s.changed.Emit(ctx, event)
}
