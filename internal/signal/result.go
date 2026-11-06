package signal

import (
	"context"
	"sync"
	"sync/atomic"
)

// Result is an immutable snapshot for one registration in an emission.
type Result[R any] struct {
	Value   R     // Business result, including partial progress on failure.
	Err     error // Admission, cancellation, timeout or callback failure.
	Started bool  // Whether the callback was actually entered.
}

// Emission holds registration-ordered completion. Forwarding attaches a callback;
// no worker or goroutine waits on a downstream operation.
type Emission[R any] struct {
	mu             sync.Mutex
	results        []Result[R]
	finished       []bool
	started        []atomic.Bool
	remaining      int
	done           chan struct{}
	callbacks      []func([]Result[R])
	cancel         context.CancelFunc
	singleResult   [1]Result[R]
	singleFinished [1]bool
	singleStarted  [1]atomic.Bool
}

// Resolved creates a completed emission containing one immutable result.
func Resolved[R any](result Result[R]) *Emission[R] {
	e := newEmission[R](func() {}, 1)
	e.finish(0, result)
	return e
}

func newEmission[R any](cancel context.CancelFunc, n int) *Emission[R] {
	e := &Emission[R]{cancel: cancel, remaining: n, done: make(chan struct{})}
	if n <= 1 {
		e.results, e.finished, e.started = e.singleResult[:n], e.singleFinished[:n], e.singleStarted[:n]
	} else {
		e.results, e.finished, e.started = make([]Result[R], n), make([]bool, n), make([]atomic.Bool, n)
	}
	if n == 0 {
		close(e.done)
		cancel()
		return e
	}
	return e
}
func (e *Emission[R]) start(i int) {
	e.started[i].Store(true)
}
func (e *Emission[R]) finish(i int, r Result[R]) {
	e.mu.Lock()
	if e.finished[i] {
		e.mu.Unlock()
		return
	}
	r.Started = r.Started || e.started[i].Load()
	e.results[i] = r
	e.finished[i] = true
	e.remaining--
	if e.remaining != 0 {
		e.mu.Unlock()
		return
	}
	callbacks := e.callbacks
	e.callbacks = nil
	close(e.done)
	e.mu.Unlock()
	for _, fn := range callbacks {
		fn(e.results)
	}
	e.cancel()
}
func (e *Emission[R]) onComplete(fn func([]Result[R])) {
	e.mu.Lock()
	if e.remaining != 0 {
		e.callbacks = append(e.callbacks, fn)
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	fn(e.results)
}
func (e *Emission[R]) Done() <-chan struct{} { return e.done }

// Wait returns receiver errors only in Result.Err; its error describes interruption of this wait.
func (e *Emission[R]) Wait(ctx context.Context) ([]Result[R], error) {
	select {
	case <-e.done:
	default:
		select {
		case <-e.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	results := append([]Result[R](nil), e.results...)
	return results, nil
}
