package cron

import (
	"context"
	"elbot/internal/events"
	"errors"
	"fmt"
	"log/slog"
)

// RefreshJob 只登记任务标识；启动前也接纳，读库和调度由 Manager 串行处理。
func (m *Manager) RefreshJob(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	m.pendingJobs[name] = struct{}{}
	select {
	case m.refreshWake <- struct{}{}:
	default:
	}
}

func (m *Manager) runRefreshes(ctx context.Context) {
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
			events.Log(ctx, events.LogRecord{
				Category: events.LogCategoryLog, Level: slog.LevelError, Module: "cron",
				EventName: "cron_job_refresh_failed", Type: events.ContentText,
				Content: fmt.Sprintf("job=%s\nerror=%v", name, err), ContentLimit: 4096,
				ResultStatus: events.ResultFailed,
			})
		}
	}
}
