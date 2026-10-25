package background

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	elcron "elbot/internal/background/cron"
	"elbot/internal/background/maintenance"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
)

func (m *Supervisor) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	if m.started {
		return nil
	}
	if err := m.cron.StartPlatformEvents(m.ctx); err != nil {
		return err
	}
	m.started = true
	if m.enableWorkers {
		m.startupDone = make(chan struct{})
		startCronAsync(m.ctx, m.cronManager, m.cfg, m.startupDone)
	}
	if m.elnis != nil {
		m.elnisDone = make(chan struct{})
		go func() {
			defer close(m.elnisDone)
			if err := m.elnis.Run(m.ctx); err != nil && err != context.Canceled {
				_, notifyErr := globalevents.NotificationRequested.EmitAsync(context.WithoutCancel(m.ctx), globalevents.NewNotification(m.ctx, slog.LevelError, "Elnis 服务异常退出："+err.Error()))
				err = errors.Join(err, notifyErr)
				_ = globalevents.EmitLog(m.ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, EventName: "platform_stopped_with_error", Module: "app", Type: globalevents.ContentText, Content: fmt.Sprintf("platform stopped with error\nplatform=elnis\nerror=%v", err), ContentLimit: 4096, ResultStatus: globalevents.ResultFailed})
			}
		}()
	}
	return nil
}

func startCronAsync(ctx context.Context, manager *elcron.Manager, cfg *config.Config, done chan<- struct{}) {
	go func() {
		defer close(done)
		startedAt := time.Now()
		if err := setupCron(ctx, manager, cfg); err != nil {
			if !errors.Is(err, context.Canceled) {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelError,
						EventName:    "cron_async_startup_failed",
						Module:       "app",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("cron async startup failed\nduration=%v\nerror=%v", time.Since(startedAt).String(), err.Error()),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					facts, _ := contextinfo.ExecutionFromContext(ctx)
					if facts.SessionID != "" {
						record.Identity.SessionID = &facts.SessionID
					}
					if facts.RunID != "" {
						record.Identity.RunID = &facts.RunID
					}
					if facts.Attempt != "" {
						record.Identity.Attempt = &facts.Attempt
					}
					if facts.RequestID != "" {
						record.Identity.RequestID = &facts.RequestID
					}
					if facts.RootRequestID != "" {
						record.Identity.RootRequestID = &facts.RootRequestID
					}
					_, notifyErr := globalevents.NotificationRequested.EmitAsync(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelError, "Cron 启动失败："+err.Error()))
					if notifyErr != nil {
						record.Content += fmt.Sprintf("\nnotification_error=%v", notifyErr)
					}
					_ = globalevents.EmitLog(ctx, record)
				}
			}
			return
		}

		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "cron_async_startup_completed",
				Module:       "app",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron async startup completed\nduration=%v", time.Since(startedAt).String()),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_ = globalevents.EmitLog(ctx, record)
		}

	}()
}

func setupCron(ctx context.Context, manager *elcron.Manager, cfg *config.Config) error {
	return maintenance.SetupCron(ctx, manager, cfg)
}

// Wait keeps an enabled Elnis runtime alive even when service mode has no foreground platform loops.
func (m *Supervisor) Wait(ctx context.Context) error {
	if m.elnisDone == nil {
		return nil
	}
	select {
	case <-m.elnisDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Supervisor) BeginClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.cancel()
	if m.cron != nil {
		m.cron.BeginClose()
	}
	var stopped context.Context
	if m.cronManager != nil {
		stopped = m.cronManager.Stop()
	}
	go func() {
		if m.startupDone != nil {
			<-m.startupDone
		}
		if stopped != nil {
			<-stopped.Done()
		}
		if m.elnisDone != nil {
			<-m.elnisDone
		}
		if m.cron != nil {
			<-m.cron.Done()
		}
		close(m.done)
	}()
}

func (m *Supervisor) Done() <-chan struct{} { return m.done }

func (m *Supervisor) Close(ctx context.Context) error {
	m.BeginClose()
	var errs []error
	if m.cron != nil {
		errs = append(errs, m.cron.Close(ctx))
	}
	select {
	case <-m.done:
	default:
		select {
		case <-m.done:
		case <-ctx.Done():
			return errors.Join(append(errs, ctx.Err())...)
		}
	}
	return errors.Join(errs...)
}
