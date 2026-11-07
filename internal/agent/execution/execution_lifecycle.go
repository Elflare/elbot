package execution

import (
	"context"
	"sync"
)

// attemptWaitLifecycle tracks joins on attempts canceled by accepted input.
type attemptWaitLifecycle struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	active int
	done   chan struct{}
}

func newAttemptWaitLifecycle(parent context.Context) *attemptWaitLifecycle {
	ctx, cancel := context.WithCancel(parent)
	l := &attemptWaitLifecycle{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	context.AfterFunc(ctx, l.beginClose)
	return l
}

func (l *attemptWaitLifecycle) begin(source context.Context) (context.Context, func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.ctx.Err() != nil {
		return nil, nil, false
	}
	l.active++
	ctx, cancel := context.WithCancel(context.WithoutCancel(source))
	stop := context.AfterFunc(l.ctx, cancel)
	if l.ctx.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
		l.mu.Lock()
		defer l.mu.Unlock()
		l.active--
		if l.closed && l.active == 0 {
			close(l.done)
		}
	}, true
}

func (l *attemptWaitLifecycle) beginClose() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.cancel()
	if l.active == 0 {
		close(l.done)
	}
}

func (l *attemptWaitLifecycle) Close(ctx context.Context) error {
	l.beginClose()
	select {
	case <-l.done:
		return nil
	default:
	}
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Execution shutdown owns append waits; entrypoints own foreground/background producers.
func (c *ExecutionCoordinator) BeginClose() {
	for _, snapshot := range c.turns.SnapshotAll() {
		c.requests.CancelSession(snapshot.SessionID)
	}
	c.turns.StopAll()
	c.attemptWaits.beginClose()
}

func (c *ExecutionCoordinator) Done() <-chan struct{} { return c.attemptWaits.done }

func (c *ExecutionCoordinator) Close(ctx context.Context) error { return c.attemptWaits.Close(ctx) }
