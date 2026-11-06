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

// emission holds registration-ordered completion. Forwarding attaches a callback;
// no worker or goroutine waits on a downstream operation.
type emission[R any] struct {
	mu             sync.Mutex
	results        []Result[R]
	finished       []bool
	started        []atomic.Bool
	remaining      int
	done           chan struct{}
	callbacks      []func([]Result[R])
	work           int      // Admitted callbacks that have not actually exited, including forwarding.
	exitCallbacks  []func() // Release upstream ownership only after actual downstream exit.
	cancel         context.CancelFunc
	singleResult   [1]Result[R]
	singleFinished [1]bool
	singleStarted  [1]atomic.Bool
}

func newEmission[R any](cancel context.CancelFunc, n int, synchronous bool) *emission[R] {
	e := &emission[R]{cancel: cancel, work: n}
	if !synchronous {
		if n == 0 {
			cancel()
		}
		return e
	}
	e.remaining = n
	e.done = make(chan struct{})
	if n <= 1 {
		e.finished, e.started = e.singleFinished[:n], e.singleStarted[:n]
		e.results = e.singleResult[:n]
	} else {
		e.finished, e.started = make([]bool, n), make([]atomic.Bool, n)
		e.results = make([]Result[R], n)
	}
	if n == 0 {
		close(e.done)
		cancel()
		return e
	}
	return e
}
func (e *emission[R]) start(i int) {
	e.started[i].Store(true)
}
func (e *emission[R]) finish(i int, r Result[R]) {
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
func (e *emission[R]) onComplete(fn func([]Result[R])) {
	e.mu.Lock()
	if e.remaining != 0 {
		e.callbacks = append(e.callbacks, fn)
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	fn(e.results)
}
func (e *emission[R]) Done() <-chan struct{} { return e.done }

func (e *emission[R]) exit() {
	e.mu.Lock()
	e.work--
	if e.work != 0 {
		e.mu.Unlock()
		return
	}
	if e.done == nil {
		e.cancel()
	}
	callbacks := e.exitCallbacks
	e.exitCallbacks = nil
	e.mu.Unlock()
	for _, fn := range callbacks {
		fn()
	}
}

func (e *emission[R]) onExit(fn func()) {
	e.mu.Lock()
	if e.work != 0 {
		e.exitCallbacks = append(e.exitCallbacks, fn)
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	fn()
}

// Wait returns receiver errors only in Result.Err; its error describes interruption of this wait.
func (e *emission[R]) Wait(ctx context.Context) ([]Result[R], error) {
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
