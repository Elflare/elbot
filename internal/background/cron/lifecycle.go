package cron

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"elbot/internal/signal"
)

// platformSubscriptions 只持有本消费者的缓存与队列；订阅连接由根部管理。
type platformSubscriptions struct {
	mu               sync.Mutex
	platformQueues   map[string]*signal.Queue
	pendingPlatforms map[string]context.Context // 启动前每个平台保留最新通知上下文。
	parent           context.Context
	stopParent       func() bool
	started          bool // 已放行通知消费。
	closed           bool
	done             chan struct{}
}

// Start 放行缓存并建立消费能力，不等待恢复任务执行完成。
func (s *Service) Start(ctx context.Context) error {
	p := &s.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return signal.ErrClosed
	}
	if p.started {
		return nil
	}
	if err := ctx.Err(); err != nil {
		p.close()
		return err
	}
	p.parent = ctx
	p.platformQueues = make(map[string]*signal.Queue)
	p.stopParent = context.AfterFunc(ctx, s.BeginClose)
	for platform, eventCtx := range p.pendingPlatforms {
		if err := s.submitPlatformConnected(eventCtx, platform); err != nil {
			p.close()
			return err
		}
	}
	clear(p.pendingPlatforms)
	p.started = true
	return nil
}

func (s *Service) EnqueuePlatformConnected(ctx context.Context, platform string) error {
	p := &s.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.started && p.parent.Err() != nil {
		return nil
	}
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return fmt.Errorf("cron: platform connection has no platform name")
	}
	ctx = context.WithoutCancel(ctx)
	if !p.started {
		if p.pendingPlatforms == nil {
			p.pendingPlatforms = make(map[string]context.Context)
		}
		p.pendingPlatforms[platform] = ctx
		return nil
	}
	return s.submitPlatformConnected(ctx, platform)
}

// submitPlatformConnected 在消费者锁内调用，同平台串行，不同平台独立运行。
func (s *Service) submitPlatformConnected(ctx context.Context, platform string) error {
	p := &s.platformEvents
	queue := p.platformQueues[platform]
	if queue == nil {
		var err error
		queue, err = signal.NewQueue(signal.QueueOptions{Name: platform + ".connected.cron"})
		if err != nil {
			return err
		}
		p.platformQueues[platform] = queue
	}
	return queue.Submit(ctx, signal.Task{Shutdown: signal.CancelPending, Run: func(runCtx context.Context) error {
		s.handlePlatformConnected(runCtx, platform)
		return nil
	}})
}

func (s *Service) BeginClose() {
	p := &s.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	p.close()
}

// close 在消费者锁内停止接纳，并让 Done 等待真实回调退出。
func (p *platformSubscriptions) close() {
	if p.closed {
		return
	}
	p.closed = true
	clear(p.pendingPlatforms)
	if p.done == nil {
		p.done = make(chan struct{})
	}
	if p.stopParent != nil {
		p.stopParent()
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

func (s *Service) Done() <-chan struct{} {
	p := &s.platformEvents
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done == nil {
		p.done = make(chan struct{})
	}
	return p.done
}

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
