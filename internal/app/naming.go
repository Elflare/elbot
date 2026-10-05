package app

import (
	"context"
	"log/slog"

	"elbot/internal/session"
	"elbot/internal/signal"
)

type namingLogger struct {
	logger *slog.Logger
}

func (n namingLogger) scheduled(ctx context.Context, event session.NamingScheduledEvent) error {
	return writeLog(ctx, n.logger, event.TriggeredAt, slog.LevelInfo, "session naming scheduled",
		"session_id", event.SessionID,
		"message_count", event.MessageCount,
		"trigger_step", event.TriggerStep,
	)
}

func (n namingLogger) completed(ctx context.Context, event session.NamingCompletedEvent) error {
	return writeLog(ctx, n.logger, event.TriggeredAt, slog.LevelInfo, "session naming completed",
		"session_id", event.SessionID,
		"title", event.Title,
		"message_count", event.MessageCount,
	)
}

func (n namingLogger) failed(ctx context.Context, event session.NamingFailedEvent) error {
	return writeLog(ctx, n.logger, event.TriggeredAt, slog.LevelWarn, "session naming failed",
		"session_id", event.SessionID,
		"stage", event.Stage,
		"llm_call", event.LLMCall,
		"reason", event.Reason,
		"invalid_reason", event.InvalidReason,
		"title", event.Title,
		"generated_title_raw", event.GeneratedTitleRaw,
		"generated_title_normalized", event.GeneratedTitleNormalized,
		"message_count", event.MessageCount,
		"failure_count", event.FailureCount,
		"max_failures", event.MaxFailures,
		"fallback_applied", event.FallbackApplied,
		"fallback_title", event.FallbackTitle,
		"error", event.Err,
	)

}

func (b *signalBindings) connectNaming(sessions *session.Service, logger *slog.Logger) error {
	queue, err := b.newQueue("session.naming_logs", logger, true)
	if err != nil {
		return err
	}
	options := signal.ConnectOptions{Executor: queue, Lifetime: signal.FollowExecutor, Shutdown: signal.Drain}
	logs, events := namingLogger{logger: logger}, sessions.NamingSignals()
	if err := connectSignal(b, events.Scheduled, logs.scheduled, options); err != nil {
		return err
	}
	if err := connectSignal(b, events.Completed, logs.completed, options); err != nil {
		return err
	}
	return connectSignal(b, events.Failed, logs.failed, options)
}
