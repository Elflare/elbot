package elnis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"elbot/internal/events"
)

// Runtime owns HTTP ingress and event/report workers, but no borrowed services.
type Runtime struct {
	server   *http.Server
	service  *Service
	queue    chan QueuedLLMEvent
	workers  int
	listener net.Listener // Bound before Start publishes readiness; owned by Serve.

	mu          sync.Mutex      // Serializes admission and lifecycle transitions.
	ctx         context.Context // Cancels HTTP handlers and all background workers.
	cancel      context.CancelFunc
	started     bool           // A single startup attempt has been claimed.
	ready       bool           // Listening and workers have been published.
	closed      bool           // No new handlers or queued work may be admitted.
	startupDone chan struct{}  // Startup has stopped publishing owned work.
	startErr    error          // Result of the first startup attempt.
	runErr      error          // Fatal Serve failure, published through failed.
	failed      chan struct{}  // Wakes observers before shutdown finishes.
	done        chan struct{}  // All owned work and final persistence have finished.
	work        sync.WaitGroup // Startup adds workers before shutdown can wait.
	connections sync.WaitGroup // StateClosed follows handler exit; Serve exit ends StateNew additions.
}

const reportRecoveryInterval = 30 * time.Second

// RecoverInterrupted also runs when Elnis is disabled; it starts no workers.
func (r *Runtime) RecoverInterrupted(ctx context.Context) error {
	return RecoverInterrupted(ctx, r.service.store)
}

// Start returns only after binding the listener and publishing its consumers.
func (r *Runtime) Start(ctx context.Context) (err error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return context.Canceled
	}
	if r.started {
		done := r.startupDone
		r.mu.Unlock()
		select {
		case <-done:
			return r.startErr
		case <-ctx.Done():
			return r.Wait(ctx)
		}
	}
	r.started = true
	r.startupDone = make(chan struct{})
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.work.Go(func() {
		<-r.ctx.Done()
		r.BeginClose()
	})
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		// Serve 可能在启动结果发布前失败并取消执行；保留具体故障。
		if errors.Is(err, context.Canceled) && r.runErr != nil {
			err = r.runErr
		}
		r.startErr = err
		close(r.startupDone)
		r.mu.Unlock()
		if err != nil {
			r.BeginClose()
		}
	}()

	if err := r.ctx.Err(); err != nil {
		return err
	}
	if err := r.service.resetDeliveringReports(r.ctx); err != nil {
		return fmt.Errorf("reset interrupted elnis reports: %w", err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(r.ctx, "tcp", r.server.Addr)
	if err != nil {
		return fmt.Errorf("listen elnis: %w", err)
	}
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		if err := listener.Close(); err != nil {
			events.Log(context.Background(), events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError,
				EventName: "elnis_listener_close_failed", Module: "background", Content: err.Error(), ResultStatus: events.LogStatus(err)})
		}
		return context.Canceled
	}
	r.listener = listener
	r.ready = true
	for i := 0; i < r.workers; i++ {
		r.work.Go(func() {
			r.worker(r.ctx)
		})
	}
	r.work.Go(func() {
		r.reportWorker(r.ctx)
	})
	r.work.Go(func() {
		err := r.server.Serve(listener)
		r.mu.Lock()
		if !r.closed || !errors.Is(err, http.ErrServerClosed) {
			r.runErr = fmt.Errorf("serve elnis: %w", err)
			close(r.failed)
		}
		r.mu.Unlock()
		r.BeginClose()
	})
	r.mu.Unlock()
	return r.ctx.Err()
}

// Wait observes a fatal run error immediately, independently of actual cleanup.
func (r *Runtime) Wait(ctx context.Context) error {
	select {
	case <-r.failed:
	case <-r.done:
	case <-ctx.Done():
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runErr != nil {
		return r.runErr
	}
	if r.startErr != nil {
		return r.startErr
	}
	select {
	case <-r.done:
		return nil
	default:
		return ctx.Err()
	}
}

// BeginClose closes admission synchronously; all blocking cleanup runs separately.
func (r *Runtime) BeginClose() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	startupDone := r.startupDone
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	go func() {
		if startupDone != nil {
			<-startupDone
		}
		// Closing transport interrupts body reads; Close itself does not join handlers.
		if err := r.server.Close(); err != nil {
			events.Log(context.Background(), events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError,
				EventName: "elnis_server_close_failed", Module: "background", Content: err.Error(), ResultStatus: events.LogStatus(err)})
		}
		r.work.Wait()
		r.connections.Wait()
		if r.ready {
			// No event writer remains. Preserve durable reports and release only unfinished execution references.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), 5*time.Second)
			if err := r.service.store.FailInterrupted(ctx,
				[]string{StatusAccepted, StatusQueued, StatusRunning}, StatusFailed, "elnis stopped before durable report"); err != nil {
				events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError,
					EventName: "elnis_shutdown_save_failed", Module: "background", Content: err.Error(), ResultStatus: events.LogStatus(err)})
			}
			cancel()
		}
		for len(r.queue) > 0 {
			<-r.queue
		}
		close(r.done)
	}()
}

func (r *Runtime) Done() <-chan struct{} { return r.done }

// Close returns cleanup errors, not the fatal run result exposed by Wait.
func (r *Runtime) Close(ctx context.Context) error {
	r.BeginClose()
	select {
	case <-r.done:
		return nil
	default:
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) enqueueLLM(ctx context.Context, event QueuedLLMEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.closed {
		return fmt.Errorf("elnis is not accepting events")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	select {
	case r.queue <- event:
		return nil
	default:
		return fmt.Errorf("elnis llm queue is full")
	}
}

func (r *Runtime) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-r.queue:
			if ctx.Err() != nil {
				return
			}
			// RunLLMEvent owns the complete task's terminal report.
			_ = r.service.RunLLMEvent(ctx, event.Event, event.EventID)
		}
	}
}

func (r *Runtime) reportWorker(ctx context.Context) {
	// Recovery and delivery own their operation diagnostics.
	r.service.recoverReports(ctx)
	ticker := time.NewTicker(reportRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.service.recoverReports(ctx)
		}
	}
}
