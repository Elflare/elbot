package signal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

var (
	ErrQueueFull = errors.New("signal: queue full")
	ErrClosed    = errors.New("signal: queue closed")
)

type QueueOptions struct {
	Name     string
	Capacity int
	Logger   *slog.Logger
	// WaitForCapacity applies backpressure instead of returning ErrQueueFull.
	WaitForCapacity bool
}
type job struct {
	ctx  context.Context
	task Task
}

type Queue struct {
	mu              sync.Mutex
	ready           *sync.Cond
	closed          bool
	jobs            []job
	capacity        int
	activeCancel    context.CancelFunc
	activeShutdown  ShutdownPolicy
	done            chan struct{}
	name            string
	logger          *slog.Logger
	waitForCapacity bool
}

func NewQueue(options QueueOptions) (*Queue, error) {
	if options.Capacity < 0 {
		return nil, errors.New("signal: negative queue capacity")
	}
	if options.Capacity == 0 {
		options.Capacity = 256
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	q := &Queue{
		jobs: make([]job, 0, options.Capacity), capacity: options.Capacity,
		done: make(chan struct{}), name: options.Name, logger: options.Logger,
		waitForCapacity: options.WaitForCapacity,
	}
	q.ready = sync.NewCond(&q.mu)
	go q.work()
	return q, nil
}

func (q *Queue) Submit(ctx context.Context, task Task) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrClosed
	}
	if task.Run == nil {
		return errors.New("signal: nil task")
	}
	if task.Shutdown != CancelPending && task.Shutdown != Drain {
		return errors.New("signal: invalid shutdown policy")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.waitForCapacity {
		stop := context.AfterFunc(ctx, func() {
			q.mu.Lock()
			q.ready.Broadcast()
			q.mu.Unlock()
		})
		defer stop()
		for len(q.jobs) == q.capacity && !q.closed && ctx.Err() == nil {
			q.ready.Wait()
		}
		if q.closed {
			return ErrClosed
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	} else if len(q.jobs) == q.capacity {
		return ErrQueueFull
	}
	q.jobs = append(q.jobs, job{ctx: ctx, task: task})
	q.ready.Broadcast()
	return nil
}

func (q *Queue) work() {
	defer close(q.done)
	for {
		q.mu.Lock()
		for len(q.jobs) == 0 && !q.closed {
			q.ready.Wait()
		}
		if len(q.jobs) == 0 {
			q.mu.Unlock()
			return
		}
		entry := q.jobs[0]
		copy(q.jobs, q.jobs[1:])
		q.jobs[len(q.jobs)-1] = job{}
		q.jobs = q.jobs[:len(q.jobs)-1]
		q.ready.Broadcast()
		ctx, cancel := context.WithCancel(entry.ctx)
		q.activeCancel = cancel
		q.activeShutdown = entry.task.Shutdown
		q.mu.Unlock()

		if ctx.Err() == nil {
			err := entry.task.Run(ctx)
			unexpected, _ := filterErrors(err, func(leaf error) bool {
				return ctx.Err() != nil && errors.Is(leaf, ctx.Err())
			})
			if unexpected != nil {
				q.logger.ErrorContext(ctx, "signal task failed", "queue", q.name, "error", unexpected)
			}
		}
		cancel()
		q.mu.Lock()
		q.activeCancel = nil
		q.mu.Unlock()
	}
}

// BeginClose stops admission and wakes blocked producers without waiting for
// callbacks. Call it before waiting for producers that may be backpressured.
func (q *Queue) BeginClose() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		kept := q.jobs[:0]
		for _, entry := range q.jobs {
			if entry.task.Shutdown == Drain {
				kept = append(kept, entry)
			}
		}
		clear(q.jobs[len(kept):])
		q.jobs = kept
		if q.activeCancel != nil && q.activeShutdown == CancelPending {
			q.activeCancel()
		}
		q.ready.Broadcast()
	}
	q.mu.Unlock()
}

// Close rejects new work and applies each subscription's shutdown policy.
// A deadline stops waiting and requests cancellation; only Done proves exit.
func (q *Queue) Close(ctx context.Context) error {
	q.BeginClose()
	select {
	case <-q.done:
		return nil
	default:
	}
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		q.mu.Lock()
		pending, active := len(q.jobs), q.activeCancel != nil
		clear(q.jobs)
		q.jobs = nil
		if q.activeCancel != nil {
			q.activeCancel()
		}
		q.ready.Broadcast()
		q.mu.Unlock()
		if q.waitForCapacity && (pending > 0 || active) {
			q.logger.ErrorContext(context.WithoutCancel(ctx), "signal log drain incomplete", "queue", q.name, "pending", pending, "active", active, "error", ctx.Err())
		}
		return fmt.Errorf("signal queue %s not fully closed: %w", q.name, ctx.Err())
	}
}

func (q *Queue) Done() <-chan struct{} { return q.done }
