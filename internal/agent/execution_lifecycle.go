package agent

import (
	"context"
	"sync"
)

// appendWaitLifecycle owns append-confirmation waits and their expiry output.
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
	if parent == nil {
		parent = context.Background()
	}
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

// BeginClose stops the Agent's event consumers and append-confirmation workers.
// Foreground/background request producers remain owned by their entrypoints.
func (a *Agent) BeginClose() {
	a.hooks.beginClosePlatformEvents()
	a.execution.appendWaits.beginClose()
	a.disconnectLogSignals()
}

// Close waits for owned workers before their dependencies may be released.
func (a *Agent) Close(ctx context.Context) error {
	a.BeginClose()
	select {
	case <-a.Done():
		return nil
	default:
	}
	select {
	case <-a.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done includes both append-expiry output and platform Hook execution.
func (a *Agent) Done() <-chan struct{} {
	a.doneOnce.Do(func() {
		a.done = make(chan struct{})
		go func() {
			<-a.execution.appendWaits.done
			a.hooks.beginClosePlatformEvents()
			<-a.hooks.platformEventsDone()
			close(a.done)
		}()
	})
	return a.done
}
