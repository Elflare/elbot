// Package signal provides typed signals with independently bounded execution.
// Publishers freeze mutable payloads before emitting them.
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
	forward    func(context.Context, T) *Emission[R]
	options    ConnectOptions
	claimed    atomic.Bool
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
	s := &Signal[T, R]{name: name, options: o, queue: q}
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
func (s *Signal[T, R]) Observe(h func(context.Context, T) error, o ConnectOptions) (*Connection, error) {
	if h == nil {
		return nil, errors.New("signal: nil handler")
	}
	return s.Connect(func(ctx context.Context, t T) (r R, err error) { err = h(ctx, t); return }, o)
}

// ConnectForward adopts a single downstream receiver's completion. The forwarder
// must emit with ctx so the operation retains its original deadline and path.
// Immediate failures and skips use Resolved; nil is a contract error.
func (s *Signal[T, R]) ConnectForward(h func(context.Context, T) *Emission[R], o ConnectOptions) (*Connection, error) {
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
	entry.connection = &Connection{disconnect: func() { s.remove(entry) }, tasks: make(map[*connectionTask]struct{}), done: make(chan struct{})}
	next := append(append([]*slot[T, R](nil), old...), entry)
	s.snapshot.Store(&snapshot[T, R]{slots: next})
	return entry.connection, nil
}
func (s *Signal[T, R]) remove(entry *slot[T, R]) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	e, err := s.EmitAsync(ctx, event)
	if e == nil {
		return nil, err
	}
	return e.Wait(context.Background())
}
func (s *Signal[T, R]) EmitAsync(ctx context.Context, event T) (*Emission[R], error) {
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
	e := newEmission[R](cancel, len(entries))
	var errs []error
	for i, entry := range entries {
		if entry.options.Once {
			if !entry.claimed.CompareAndSwap(false, true) {
				e.finish(i, Result[R]{})
				continue
			}
			s.remove(entry)
		}
		var value R
		forwarded := false
		taskCtx, taskCancel := context.WithCancel(ctx)
		parentStart, _ := ctx.Value(startKey{}).(*startPath)
		if entry.forward != nil {
			taskCtx = context.WithValue(taskCtx, startKey{}, &startPath{started: &e.started[i], parent: parentStart})
		}
		owned := entry.connection.acquire(taskCancel)
		if owned == nil {
			taskCancel()
			e.finish(i, Result[R]{Err: ErrClosed})
			errs = append(errs, ErrClosed)
			continue
		}
		stopTask := context.AfterFunc(taskCtx, func() { e.finish(i, Result[R]{Err: taskCtx.Err()}) })
		task := Task{cancel: taskCancel, Shutdown: entry.options.Shutdown, Run: func(runCtx context.Context) error {
			if err := runCtx.Err(); err != nil {
				return err
			}
			if s.options.Before != nil {
				if err := s.options.Before(taskCtx, event); err != nil {
					return err
				}
			}
			if entry.forward != nil {
				child := entry.forward(taskCtx, event)
				if child == nil {
					return errors.New("signal: nil forwarded emission")
				}
				if len(child.results) != 1 {
					return errors.New("signal: forwarding requires one downstream receiver")
				}
				forwarded = true
				child.onComplete(func(results []Result[R]) { stopTask(); e.finish(i, results[0]); taskCancel() })
				return nil
			}
			e.start(i)
			for p := parentStart; p != nil; p = p.parent {
				p.started.Store(true)
			}
			var err error
			value, err = entry.handler(taskCtx, event)
			return err
		}, Finish: func(err error) {
			defer entry.connection.release(owned)
			if !forwarded {
				stopTask()
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrQueueFull) {
					reportFailure("signal callback failed", slog.String("signal", s.name), slog.Any("error", err))
				}
				e.finish(i, Result[R]{Value: value, Err: err})
				taskCancel()
			}
		}}
		if s.options.Key != nil {
			task.Key = s.options.Key(event)
		}
		if err := s.queue.Submit(taskCtx, task); err != nil {
			task.Finish(err)
			errs = append(errs, err)
		}
	}
	return e, errors.Join(errs...)
}
func (s *Signal[T, R]) BeginClose() {
	s.mu.Lock()
	s.closed.Store(true)
	s.mu.Unlock()
	s.queue.BeginClose()
}
func (s *Signal[T, R]) Close(ctx context.Context) error { s.BeginClose(); return s.queue.Close(ctx) }
func (s *Signal[T, R]) Done() <-chan struct{}           { return s.queue.Done() }
