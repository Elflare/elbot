package cron

import (
	"context"
	"fmt"
	"strings"
	"sync"

	globalevents "elbot/internal/events"
	"elbot/internal/signal"
)

// platformSubscriptions belongs to this consumer, including all per-platform workers.
type platformSubscriptions struct {
	mu                 sync.Mutex
	platformConnection *signal.Connection
	platformQueues     map[string]*signal.Queue
	parent             context.Context
	stopParent         func() bool
	closed             bool
	done               chan struct{}
}

// StartPlatformEvents subscribes after dependencies are ready and before adapters start.
// Repeated starts do not duplicate subscriptions; a closed consumer cannot restart.
func (s *Service) StartPlatformEvents(ctx context.Context) error {
	p := &s.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return signal.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.platformConnection != nil {
		return nil
	}
	connection, err := globalevents.PlatformConnected.Connect(s.enqueuePlatformConnected, signal.ConnectOptions{})
	if err != nil {
		return err
	}
	p.platformConnection = connection
	p.platformQueues = make(map[string]*signal.Queue)
	p.parent = ctx
	p.stopParent = context.AfterFunc(ctx, s.BeginClose)
	return nil
}

func (s *Service) enqueuePlatformConnected(ctx context.Context, event globalevents.PlatformConnectedEvent) error {
	p := &s.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	// Disconnect cannot retract an Emit snapshot. Check admission under the same
	// lock as queue creation and shutdown, including cancellation before AfterFunc runs.
	if p.closed || p.platformConnection == nil || p.parent.Err() != nil {
		return nil
	}
	if strings.TrimSpace(event.Platform) == "" {
		return fmt.Errorf("cron: platform connection has no platform name")
	}
	queue := p.platformQueues[event.Platform]
	if queue == nil {
		var err error
		queue, err = signal.NewQueue(signal.QueueOptions{Name: event.Platform + ".connected.cron"})
		if err != nil {
			return err
		}
		p.platformQueues[event.Platform] = queue
	}
	// Match FollowExecutor: retain emission values, but cancellation belongs to
	// this consumer. Submit is bounded and never waits for capacity or business work.
	return queue.Submit(context.WithoutCancel(ctx), signal.Task{
		Shutdown: signal.CancelPending,
		Run: func(runCtx context.Context) error {
			s.handlePlatformConnected(runCtx, event.Platform)
			return nil
		},
	})
}

// BeginClose disconnects and cancels workers without waiting for their callbacks.
func (s *Service) BeginClose() {
	p := &s.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if p.done == nil {
		p.done = make(chan struct{})
	}
	if p.stopParent != nil {
		p.stopParent()
	}
	if p.platformConnection != nil {
		p.platformConnection.Disconnect()
	}
	queues := make([]*signal.Queue, 0, len(p.platformQueues))
	for _, queue := range p.platformQueues {
		queue.BeginClose()
		queues = append(queues, queue)
	}
	if len(queues) == 0 {
		close(p.done)
		return
	}
	go func() {
		for _, queue := range queues {
			<-queue.Done()
		}
		close(p.done)
	}()
}

// Done closes only after admission has stopped and every callback has exited.
func (s *Service) Done() <-chan struct{} {
	p := &s.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done == nil {
		p.done = make(chan struct{})
	}
	return p.done
}

// Close stops connection recovery and waits within the caller's shutdown budget.
// The scheduler itself is owned and stopped separately by Manager.
func (s *Service) Close(ctx context.Context) error {
	s.BeginClose()
	select {
	case <-s.Done():
		return nil
	default:
	}
	select {
	case <-s.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
