package execution

import (
	"context"
	"sync"
	"time"
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

func (c *ExecutionCoordinator) expireAppendAfter(ctx context.Context, sessionID string, waitID uint64) {
	if c.appendWait == nil {
		return
	}
	timeout := c.appendWait.WaitTimeout(ctx)
	if timeout <= 0 {
		return
	}
	waitCtx, finish, accepted := c.attemptWaits.begin(ctx)
	if !accepted {
		return
	}
	go func() {
		defer finish()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-waitCtx.Done():
			return
		}
		_, release, err := c.sessions.EnterSessions(waitCtx, sessionID)
		if err != nil {
			return
		}
		expired := c.turns.ExpireAppend(sessionID, waitID)
		release()
		if expired {
			c.sessionIdle(waitCtx, sessionID)
		}
	}()
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
