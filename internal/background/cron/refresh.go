package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	background "elbot/internal/background/share"
	"elbot/internal/events"
)

// RefreshJob 只登记任务标识；启动前也接纳，读库和调度由 Manager 串行处理。
func (m *Manager) RefreshJob(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil
	}
	m.pendingJobs[name] = struct{}{}
	select {
	case m.refreshWake <- struct{}{}:
	default:
	}
	return nil
}

func (m *Manager) runRefreshes(ctx context.Context) {
	defer m.workers.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.refreshWake:
		}
		m.mu.Lock()
		pending := m.pendingJobs
		m.pendingJobs = make(map[string]struct{})
		m.mu.Unlock()
		m.refreshPending(ctx, pending)
	}
}

func (m *Manager) refreshPending(ctx context.Context, pending map[string]struct{}) {
	for name := range pending {
		if ctx.Err() != nil {
			return
		}
		job, err := m.repo.GetByName(ctx, name)
		if errors.Is(err, ErrJobNotFound) {
			m.removeEntry(name)
			continue
		}
		if err == nil {
			err = m.scheduleJob(ctx, *job)
		}
		if err != nil && ctx.Err() == nil {
			background.LogBackground(ctx, events.LogRecord{
				Category: events.LogRuntime, Level: slog.LevelError, Module: "cron",
				EventName: "cron_job_refresh_failed", Type: events.ContentText,
				Content: fmt.Sprintf("job=%s\nerror=%v", name, err), ContentLimit: 4096,
				ResultStatus: events.ResultFailed,
			})
		}
	}
}
