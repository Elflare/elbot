// Package signal provides typed signals with independently bounded execution.
// Publishers freeze mutable payloads before emitting them.
// Synchronous handlers return business results unchanged; observers handle their own failures.
package signal

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

var ErrNoReceiver = errors.New("signal: no receiver")
var ErrRecursive = errors.New("signal: recursive dependency")

type Handler[T, R any] func(context.Context, T) (R, error)
type slot[T, R any] struct {
	handler    Handler[T, R]
	observer   func(context.Context, T) // Receives facts and owns any business failure handling.
	forward    func(context.Context, T) (context.Context, *Signal[T, R], T)
	options    ConnectOptions
	connection *Connection
}
type snapshot[T, R any] struct{ slots []*slot[T, R] }
type callPath struct {
	signal any
	parent *callPath
}
type pathKey struct{}
type startKey struct{}
type startPath struct {
	started *atomic.Bool
	parent  *startPath
}

type Signal[T, R any] struct {
	mu       sync.Mutex
	name     string
	options  Options[T]
	snapshot atomic.Pointer[snapshot[T, R]]
	queue    *Queue
	closed   atomic.Bool
	done     chan struct{} // Closed after the queue and any transferred callbacks actually exit.
}

func New[T, R any](name string, options ...Options[T]) *Signal[T, R] {
	var o Options[T]
	if len(options) > 0 {
		o = options[0]
	}
	if o.Concurrency == 0 {
		o.Concurrency = 8
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	q, err := NewQueue(QueueOptions{Name: name, Capacity: o.Capacity, Concurrency: o.Concurrency})
	if err != nil || o.Timeout < 0 {
		panic("signal: invalid options")
	}
	s := &Signal[T, R]{name: name, options: o, queue: q, done: make(chan struct{})}
	s.snapshot.Store(&snapshot[T, R]{})
	return s
}
func (s *Signal[T, R]) Connect(h Handler[T, R], o ConnectOptions) (*Connection, error) {
	if h == nil {
		return nil, errors.New("signal: nil handler")
	}
	return s.connect(&slot[T, R]{handler: h, options: o})
}

// Observe connects a fact observer without a business return value.
func (s *Signal[T, R]) Observe(h func(context.Context, T), o ConnectOptions) (*Connection, error) {
	if h == nil {
		return nil, errors.New("signal: nil handler")
	}
	return s.connect(&slot[T, R]{observer: h, options: o})
}

// ConnectForward routes to one downstream receiver. A nil destination skips delivery.
// The framework retains the original deadline and cancellation until downstream completion.
func (s *Signal[T, R]) ConnectForward(h func(context.Context, T) (context.Context, *Signal[T, R], T), o ConnectOptions) (*Connection, error) {
	if h == nil {
		return nil, errors.New("signal: nil forwarder")
	}
	return s.connect(&slot[T, R]{forward: h, options: o})
}
func (s *Signal[T, R]) connect(entry *slot[T, R]) (*Connection, error) {
	if entry.options.Shutdown != CancelPending && entry.options.Shutdown != Drain {
		return nil, errors.New("signal: invalid shutdown policy")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return nil, ErrClosed
	}
	old := s.snapshot.Load().slots
	if s.options.Single && len(old) != 0 {
		return nil, errors.New("signal: receiver already connected")
	}
	entry.connection = &Connection{disconnect: func() { s.remove(entry) }, tasks: make(map[*connectionTask]struct{}), shutdown: entry.options.Shutdown, done: make(chan struct{})}
	next := append(append([]*slot[T, R](nil), old...), entry)
	s.snapshot.Store(&snapshot[T, R]{slots: next})
	return entry.connection, nil
}
func (s *Signal[T, R]) remove(entry *slot[T, R]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Retain the closing snapshot so repeated Close still joins transferred callbacks.
	if s.closed.Load() {
		return
	}
	old := s.snapshot.Load().slots
	for i, v := range old {
		if v == entry {
			next := make([]*slot[T, R], 0, len(old)-1)
			next = append(next, old[:i]...)
			next = append(next, old[i+1:]...)
			s.snapshot.Store(&snapshot[T, R]{slots: next})
			return
		}
	}
}
func (s *Signal[T, R]) Emit(ctx context.Context, event T) ([]Result[R], error) {
	if len(s.snapshot.Load().slots) == 0 && !s.options.Required {
		if s.closed.Load() {
			return nil, ErrClosed
		}
		return nil, ctx.Err()
	}
	e, err := s.emit(ctx, event, true)
	if e == nil {
		return nil, err
	}
	results, waitErr := e.Wait(context.Background())
	if err != nil {
		return results, err
	}
	return results, waitErr
}

// EmitAsync submits observers without exposing completion or business results.
func (s *Signal[T, R]) EmitAsync(ctx context.Context, event T) {
	if _, err := s.emit(ctx, event, false); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) {
		ReportDiagnostic("signal admission failed", slog.String("signal", s.name), slog.Any("error", err))
	}
}

// emit shares admission and lifetime management; only synchronous emissions collect results.
func (s *Signal[T, R]) emit(ctx context.Context, event T, synchronous bool) (*emission[R], error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, _ := ctx.Value(pathKey{}).(*callPath)
	for p := path; p != nil; p = p.parent {
		if p.signal == s {
			return nil, ErrRecursive
		}
	}
	ctx = context.WithValue(ctx, pathKey{}, &callPath{signal: s, parent: path})
	ctx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	entries := s.snapshot.Load().slots
	if s.closed.Load() {
		cancel()
		return nil, ErrClosed
	}
	if len(entries) == 0 && s.options.Required {
		cancel()
		return nil, ErrNoReceiver
	}
	if !synchronous {
		for _, entry := range entries {
			if entry.handler != nil {
				cancel()
				return nil, errors.New("signal: asynchronous emission requires observers")
			}
		}
	}
	e := newEmission[R](cancel, len(entries), synchronous)
	for i, entry := range entries {
		var value R
		forwarded := false
		taskCtx, taskCancel := context.WithCancel(ctx)
		var parentStart *startPath
		if synchronous {
			parentStart, _ = ctx.Value(startKey{}).(*startPath)
		}
		if synchronous && entry.forward != nil {
			taskCtx = context.WithValue(taskCtx, startKey{}, &startPath{started: &e.started[i], parent: parentStart})
		}
		owned := entry.connection.acquire(taskCancel)
		if owned == nil {
			taskCancel()
			for j := i; j < len(entries); j++ {
				if synchronous {
					e.finish(j, Result[R]{Err: ErrClosed})
				}
				e.exit()
			}
			return e, ErrClosed
		}
		var stopTask func() bool
		if synchronous {
			stopTask = context.AfterFunc(taskCtx, func() { e.finish(i, Result[R]{Err: taskCtx.Err()}) })
		}
		task := Task{cancel: taskCancel, Shutdown: entry.options.Shutdown, Run: func(runCtx context.Context) error {
			if entry.forward != nil {
				ctx, target, event := entry.forward(taskCtx, event)
				if target == nil {
					return nil
				}
				if !target.options.Single {
					return errors.New("signal: forwarding requires a single-receiver destination")
				}
				child, err := target.emit(ctx, event, synchronous)
				if err != nil {
					return err
				}
				forwarded = true
				child.onExit(func() { taskCancel(); entry.connection.release(owned); e.exit() })
				if synchronous {
					child.onComplete(func(results []Result[R]) {
						stopTask()
						var result Result[R]
						// Optional destinations may have no receiver.
						if len(results) != 0 {
							result = results[0]
						}
						e.finish(i, result)
						taskCancel()
					})
				}
				return nil
			}
			if synchronous {
				e.start(i)
				for p := parentStart; p != nil; p = p.parent {
					p.started.Store(true)
				}
			}
			if entry.observer != nil {
				entry.observer(taskCtx, event)
				return nil
			}
			var err error
			value, err = entry.handler(taskCtx, event)
			return err
		}, Finish: func(err error) {
			if !forwarded {
				defer e.exit()
				defer entry.connection.release(owned)
				if synchronous {
					stopTask()
					e.finish(i, Result[R]{Value: value, Err: err})
				} else if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) {
					ReportDiagnostic("signal delivery failed", slog.String("signal", s.name), slog.Any("error", err))
				}
				taskCancel()
			}
		}}
		if s.options.Key != nil {
			task.Key = s.options.Key(event)
		}
		if err := s.queue.Submit(taskCtx, task); err != nil {
			if synchronous {
				stopTask()
			}
			entry.connection.release(owned)
			taskCancel()
			for j := i; j < len(entries); j++ {
				if synchronous {
					e.finish(j, Result[R]{Err: err})
				}
				e.exit()
			}
			return e, err
		}
	}
	return e, nil
}
func (s *Signal[T, R]) BeginClose() {
	s.mu.Lock()
	if s.closed.Swap(true) {
		s.mu.Unlock()
		return
	}
	entries := s.snapshot.Load().slots
	s.mu.Unlock()
	for _, entry := range entries {
		entry.connection.BeginClose()
	}
	s.queue.BeginClose()
	go func() {
		<-s.queue.Done()
		for _, entry := range entries {
			<-entry.connection.Done()
		}
		close(s.done)
	}()
}
func (s *Signal[T, R]) Close(ctx context.Context) error {
	s.BeginClose()
	if err := s.queue.Close(ctx); err != nil {
		return err
	}
	for _, entry := range s.snapshot.Load().slots {
		if err := entry.connection.Close(ctx); err != nil {
			return err
		}
	}
	<-s.done
	return nil
}
func (s *Signal[T, R]) Done() <-chan struct{} { return s.done }
