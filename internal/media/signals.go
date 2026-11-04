package media

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/signal"
)

// connectUsageFinished 由根部装配，transfer 不依赖信号系统。
func (m *MediaCenter) connectUsageFinished() error {
	m.usageFinished = signal.New[[]string, signal.Empty]("media.usage_finished", signal.Options[[]string]{Concurrency: 1})
	connection, err := m.usageFinished.Observe(m.transfer.CleanupUsages, signal.ConnectOptions{Shutdown: signal.Drain})
	m.usageConnection = connection
	return err
}

// PublishUsageFinished 先保存结束事实，再唤醒清理；拒收或超时不丢失记录。
// 结束通知隔离业务取消，错误留在 Media，不改变已完成的业务结果。
func (m *MediaCenter) PublishUsageFinished(ctx context.Context, event UsageFinishedEvent) {
	ids := make([]string, 0, len(event.UsageIDs))
	for _, id := range event.UsageIDs {
		if id != "" {
			ids = append(ids, string(id))
		}
	}
	if len(ids) == 0 {
		return
	}
	m.transfer.FinishUsages(ids)
	if _, err := m.usageFinished.EmitAsync(context.WithoutCancel(ctx), ids); err != nil {
		slog.WarnContext(context.WithoutCancel(ctx), "media usage notification failed; maintenance retains cleanup", "error", err)
	}
}

// Close 在全部使用方退出后排空信号、解除订阅，并清理已结束资源。
// 超时保留状态供后续关闭继续等待，不关闭借用存储。
func (m *MediaCenter) Close(ctx context.Context) error {
	m.closed.Store(true)
	if m.usageFinished != nil {
		if err := m.usageFinished.Close(ctx); err != nil {
			return err
		}
	}
	if m.usageConnection != nil {
		if err := m.usageConnection.Close(ctx); err != nil {
			return err
		}
	}
	var errs []error
	if m.transfer != nil {
		errs = append(errs, m.transfer.CleanupUsages(ctx, nil))
	}
	if m.objects != nil {
		errs = append(errs, m.objects.Close(ctx))
	}
	return errors.Join(errs...)
}
