package execution

import (
	"context"
	"sync"
)

// appendWaitLifecycle owns confirmation waits, attempt joins and expiry output.
// Their routing values survive a request, but cancellation belongs to the app.
type appendWaitLifecycle struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	active int
	done   chan struct{}
}

func newAppendWaitLifecycle(parent context.Context) *appendWaitLifecycle {
	ctx, cancel := context.WithCancel(parent)
	l := &appendWaitLifecycle{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	context.AfterFunc(ctx, l.beginClose)
	return l
}

func (l *appendWaitLifecycle) begin(source context.Context) (context.Context, func(), bool) {
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
	return ctx, sync.OnceFunc(func() {
		stop()
		cancel()
		l.mu.Lock()
		defer l.mu.Unlock()
		l.active--
		if l.closed && l.active == 0 {
			close(l.done)
		}
	}), true
}

func (l *appendWaitLifecycle) beginClose() {
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

func (l *appendWaitLifecycle) Close(ctx context.Context) error {
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
func (c *ExecutionCoordinator) BeginClose() { c.appendWaits.beginClose() }

func (c *ExecutionCoordinator) Done() <-chan struct{} { return c.appendWaits.done }

func (c *ExecutionCoordinator) Close(ctx context.Context) error { return c.appendWaits.Close(ctx) }
