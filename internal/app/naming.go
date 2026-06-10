package app

import (
	"context"
	"log/slog"

	"elbot/internal/session"
)

type namingLogger struct {
	logger *slog.Logger
}

func (n namingLogger) NotifyNamingScheduled(ctx context.Context, event session.NamingScheduledEvent) {
	if n.logger == nil {
		return
	}
	n.logger.InfoContext(ctx, "session naming scheduled",
		"session_id", event.SessionID,
		"message_count", event.MessageCount,
		"trigger_step", event.TriggerStep,
	)
}

func (n namingLogger) NotifyNamingCompleted(ctx context.Context, event session.NamingCompletedEvent) {
	if n.logger == nil {
		return
	}
	n.logger.InfoContext(ctx, "session naming completed",
		"session_id", event.SessionID,
		"title", event.Title,
		"message_count", event.MessageCount,
	)
}

func (n namingLogger) NotifyNamingFailed(ctx context.Context, event session.NamingFailedEvent) {
	if n.logger == nil {
		return
	}
	n.logger.WarnContext(ctx, "session naming failed",
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
