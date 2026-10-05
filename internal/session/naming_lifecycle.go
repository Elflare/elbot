package session

import (
	"context"
	"sync"
)

type namingLifecycle struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	stopParent func() bool
	stopped    bool
	active     int
	done       chan struct{}
}

// StartNaming binds naming workers to the application, not the triggering Turn.
// A closed service cannot be restarted.
func (s *Service) StartNaming(ctx context.Context) {
	n := &s.naming
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stopped || n.ctx != nil {
		return
	}
	n.ctx, n.cancel = context.WithCancel(ctx)
	n.stopParent = context.AfterFunc(ctx, s.stopNaming)
}

func (s *Service) stopNaming() {
	n := &s.naming
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stopped {
		return
	}
	n.stopped = true
	if n.stopParent != nil {
		n.stopParent()
	}
	if n.cancel != nil {
		n.cancel()
	}
	if n.active == 0 {
		close(n.done)
	}
}

// Close cancels naming immediately and waits only within the caller's budget.
// Done remains open until all admitted work, including preparation, has exited.
func (s *Service) Close(ctx context.Context) error {
	s.stopNaming()
	select {
	case <-s.Done():
		return nil
	default:
	}
	select {
	case <-s.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Done() <-chan struct{} { return s.naming.done }

func (s *Service) beginNaming() (context.Context, func(), bool) {
	n := &s.naming
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stopped || n.ctx == nil || n.ctx.Err() != nil {
		return nil, nil, false
	}
	n.active++
	return n.ctx, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.active--
		if n.stopped && n.active == 0 {
			close(n.done)
		}
	}, true
}
