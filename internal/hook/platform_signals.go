// 管理平台连接订阅、按平台排队执行、输出及订阅释放。

package hook

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
	"elbot/internal/signal"
)

// platformSubscriptions owns only this consumer's connection and business queues.
// The process-wide platform event source belongs to the publishing subsystem.
type platformSubscriptions struct {
	mu                 sync.Mutex
	platformConnection *signal.Connection
	platformQueues     map[string]*signal.Queue // One serial queue per platform; platforms do not block each other.
	parent             context.Context          // Consumer lifetime, independent of an individual connection emission.
	stopParent         func() bool
	closed             bool
	done               chan struct{} // The connection and every accepted queue callback have actually exited.
}

func (m *Supervisor) startPlatformEvents(ctx context.Context) error {
	p := &m.platformEvents
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
	connection, err := events.PlatformConnected.Observe(m.enqueuePlatformConnected, signal.ConnectOptions{})
	if err != nil {
		return err
	}
	p.platformConnection = connection
	p.platformQueues = make(map[string]*signal.Queue)
	p.parent = ctx
	p.stopParent = context.AfterFunc(ctx, m.beginClosePlatformEvents)
	return nil
}

func (m *Supervisor) enqueuePlatformConnected(ctx context.Context, event events.PlatformConnectedEvent) error {
	p := &m.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	// An emission snapshot may outlive disconnection. Admission and queue creation
	// share the shutdown lock; platform event validity belongs to its publisher.
	if p.closed || p.parent.Err() != nil {
		return nil
	}
	queue := p.platformQueues[event.Platform]
	if queue == nil {
		var err error
		queue, err = signal.NewQueue(signal.QueueOptions{Name: event.Platform + ".connected.hooks"})
		if err != nil {
			return err
		}
		p.platformQueues[event.Platform] = queue
	}
	return queue.Submit(context.WithoutCancel(ctx), signal.Task{
		Shutdown: signal.CancelPending,
		Run: func(ctx context.Context) error {
			m.handlePlatformConnected(ctx, event.Platform)
			return nil
		},
	})
}

func (m *Supervisor) handlePlatformConnected(ctx context.Context, platformName string) {
	// Global platform work is independent of Agent registration and never creates
	// an Agent Request, even when this Hook supervisor is connected to an Agent.
	event, _, err := m.run(ctx, share.Event{
		Point:    share.PointPlatformConnected,
		Platform: share.PlatformContext{Name: platformName},
	}, pipeline.CallOptions{Woken: true})
	if err != nil || len(event.Outputs) == 0 {
		return
	}
	_, result, err := m.send(ctx, delivery.Target{}, event.Outputs)
	// This boundary owns delivery of the global Hook's outputs. Preserve the
	// sender's declared outcome independently of its business error.
	level := slog.LevelInfo
	if result.ResultStatus == events.ResultFailed {
		level = slog.LevelError
	}
	content := fmt.Sprintf("platform=%s\n%s", platformName, result.Content)
	if err != nil {
		content += "\nbusiness_error=" + err.Error()
	}
	LogHook(ctx, events.LogRecord{
		Category: events.LogRuntime, Module: "hook", EventName: "hook_output",
		Level: level, Type: events.ContentText, ContentLimit: 4096,
		Content: content, ResultStatus: result.ResultStatus,
	})
}

func (m *Supervisor) beginClosePlatformEvents() {
	p := &m.platformEvents
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
		p.platformConnection.BeginClose()
	}
	queues := make([]*signal.Queue, 0, len(p.platformQueues))
	for _, queue := range p.platformQueues {
		queue.BeginClose()
		queues = append(queues, queue)
	}
	go func() {
		if p.platformConnection != nil {
			<-p.platformConnection.Done()
		}
		for _, queue := range queues {
			<-queue.Done()
		}
		close(p.done)
	}()
}

func (m *Supervisor) platformEventsDone() <-chan struct{} {
	p := &m.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done == nil {
		p.done = make(chan struct{})
	}
	return p.done
}
