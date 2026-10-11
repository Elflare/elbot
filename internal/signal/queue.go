package signal

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

var (
	ErrQueueFull = errors.New("signal: queue full")
	ErrClosed    = errors.New("signal: queue closed")
)

type QueueOptions struct {
	Name             string
	Capacity         int           // Waiting callbacks; running callbacks have a separate bound.
	Concurrency      int           // Business queues default to one; signals explicitly use eight.
	WaitForCapacity  bool          // Wait for admission, bounded by context or AdmissionTimeout.
	AdmissionTimeout time.Duration // Does not become the admitted task's deadline.
}
type lane struct {
	key    string
	jobs   list.List
	ready  *list.Element
	active int
}
type job struct {
	ctx     context.Context
	task    Task
	lane    *lane
	element *list.Element
	stop    func() bool
	cancel  context.CancelFunc
}

// Queue schedules runnable lanes. Waiting for a key never occupies a worker.
// Idle workers retire; admission starts at most concurrency workers under mu.
type Queue struct {
	mu                    sync.Mutex
	space                 *sync.Cond
	ready                 list.List
	lanes                 map[string]*lane
	active                map[*job]struct{}
	pending, workers      int
	finishing             int
	capacity, concurrency int
	closed                bool
	done                  chan struct{}
	doneOnce              sync.Once
	name                  string
	waitForCapacity       bool
	admissionTimeout      time.Duration
}

func NewQueue(o QueueOptions) (*Queue, error) {
	if o.Capacity < 0 || o.Concurrency < 0 || o.AdmissionTimeout < 0 {
		return nil, errors.New("signal: negative queue option")
	}
	if o.Capacity == 0 {
		o.Capacity = 256
	}
	if o.Concurrency == 0 {
		o.Concurrency = 1
	}
	q := &Queue{capacity: o.Capacity, concurrency: o.Concurrency, name: o.Name, done: make(chan struct{}), lanes: make(map[string]*lane), active: make(map[*job]struct{}), waitForCapacity: o.WaitForCapacity, admissionTimeout: o.AdmissionTimeout}
	q.space = sync.NewCond(&q.mu)
	return q, nil
}
func (q *Queue) Submit(ctx context.Context, task Task) error {
	if task.Run == nil {
		return errors.New("signal: nil task")
	}
	if task.Shutdown != CancelPending && task.Shutdown != Drain {
		return errors.New("signal: invalid shutdown policy")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.pending == q.capacity {
		if !q.waitForCapacity {
			return ErrQueueFull
		}
		admission := ctx
		if q.admissionTimeout > 0 {
			var cancel context.CancelFunc
			admission, cancel = context.WithTimeout(ctx, q.admissionTimeout)
			defer cancel()
		}
		stop := context.AfterFunc(admission, func() { q.mu.Lock(); q.space.Broadcast(); q.mu.Unlock() })
		defer stop()
		for q.pending == q.capacity && !q.closed && admission.Err() == nil {
			q.space.Wait()
		}
		if q.closed {
			return ErrClosed
		}
		if err := admission.Err(); err != nil {
			return fmt.Errorf("signal admission: %w", err)
		}
	}
	l := q.lanes[task.Key]
	if l == nil {
		l = &lane{key: task.Key}
		q.lanes[task.Key] = l
	}
	j := &job{ctx: ctx, task: task, lane: l}
	j.element = l.jobs.PushBack(j)
	q.pending++
	if ctx.Done() != nil {
		j.stop = context.AfterFunc(ctx, func() { q.cancelPending(j, ctx.Err()) })
	}
	q.makeReady(l)
	q.startWorkers()
	return nil
}
func (q *Queue) makeReady(l *lane) {
	if l.jobs.Len() > 0 && l.ready == nil && (l.key == "" || l.active == 0) {
		l.ready = q.ready.PushBack(l)
	}
	if l.key != "" && l.jobs.Len() == 0 && l.active == 0 {
		delete(q.lanes, l.key)
	}
}
func (q *Queue) startWorkers() {
	if q.workers == q.concurrency {
		return
	}
	// Count runnable lanes, not pending jobs behind a busy key. Workers already
	// launched but not yet running will consume part of this ready work.
	n := len(q.active) - q.workers
	for e := q.ready.Front(); e != nil && n < q.concurrency-q.workers; e = e.Next() {
		l := e.Value.(*lane)
		if l.key == "" {
			n += l.jobs.Len()
		} else {
			n++
		}
	}
	for q.workers < q.concurrency && n > 0 {
		q.workers++
		n--
		go q.work()
	}
}
func (q *Queue) cancelPending(j *job, err error) {
	q.mu.Lock()
	if j.element == nil {
		q.mu.Unlock()
		return
	}
	q.removePending(j)
	q.finishing++
	q.mu.Unlock()
	if j.task.Finish != nil {
		j.task.Finish(err)
	}
	q.mu.Lock()
	q.finishing--
	q.finishClosed()
	q.mu.Unlock()
}
func (q *Queue) removePending(j *job) {
	l := j.lane
	l.jobs.Remove(j.element)
	j.element = nil
	q.pending--
	if j.stop != nil {
		j.stop()
	}
	if l.jobs.Len() == 0 && l.ready != nil {
		q.ready.Remove(l.ready)
		l.ready = nil
	}
	q.makeReady(l)
	q.space.Broadcast()
}
func runTask(ctx context.Context, task Task) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("signal callback panic: %v", p)
		}
	}()
	if err = ctx.Err(); err == nil {
		err = task.Run(ctx)
	}
	return
}
func (q *Queue) work() {
	for {
		q.mu.Lock()
		if q.ready.Len() == 0 {
			q.workers--
			q.finishClosed()
			q.mu.Unlock()
			return
		}
		l := q.ready.Front().Value.(*lane)
		q.ready.Remove(l.ready)
		l.ready = nil
		j := l.jobs.Front().Value.(*job)
		l.active++
		q.removePending(j)
		ctx, cancel := j.ctx, j.task.cancel
		if cancel == nil {
			ctx, cancel = context.WithCancel(ctx)
		}
		j.cancel = cancel
		q.active[j] = struct{}{}
		q.mu.Unlock()
		err := runTask(ctx, j.task)
		if j.task.Finish != nil {
			j.task.Finish(err)
		} else if err != nil && !errors.Is(err, ctx.Err()) {
			reportFailure("signal task failed", slog.String("queue", q.name), slog.Any("error", err))
		}
		if j.task.cancel == nil {
			cancel()
		}
		q.mu.Lock()
		delete(q.active, j)
		l.active--
		q.makeReady(l)
		q.mu.Unlock()
	}
}
func (q *Queue) finishClosed() {
	if q.closed && q.workers == 0 && q.pending == 0 && q.finishing == 0 {
		q.doneOnce.Do(func() { close(q.done) })
	}
}
func (q *Queue) stop(all bool) (pending int, active bool) {
	q.mu.Lock()
	q.closed = true
	var removed []*job
	for _, l := range q.lanes {
		for e := l.jobs.Front(); e != nil; {
			next := e.Next()
			j := e.Value.(*job)
			if all || j.task.Shutdown == CancelPending {
				q.removePending(j)
				removed = append(removed, j)
			}
			e = next
		}
	}
	for j := range q.active {
		if j.task.Shutdown == Drain {
			active = true
		}
		if all || j.task.Shutdown == CancelPending {
			j.cancel()
		}
	}
	q.space.Broadcast()
	q.finishing += len(removed)
	q.finishClosed()
	q.mu.Unlock()
	for _, j := range removed {
		if j.task.Finish != nil {
			j.task.Finish(ErrClosed)
		}
	}
	q.mu.Lock()
	q.finishing -= len(removed)
	q.finishClosed()
	q.mu.Unlock()
	return len(removed), active
}
func (q *Queue) BeginClose() { q.stop(false) }
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
		pending, active := q.stop(true)
		if pending > 0 || active {
			reportFailure("signal drain incomplete", slog.String("queue", q.name), slog.Int("pending", pending), slog.Bool("active", active), slog.Any("error", ctx.Err()))
		}
		return fmt.Errorf("signal queue %s not fully closed: %w", q.name, ctx.Err())
	}
}
func (q *Queue) Done() <-chan struct{} { return q.done }
