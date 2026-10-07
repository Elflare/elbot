package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"elbot/internal/events"
	"elbot/internal/signal"
)

type namingLogProjection struct{}

func (n namingLogProjection) scheduled(ctx context.Context, event NamingScheduledEvent) error {
	emitNamingLog(ctx, event.TriggeredAt, events.LogRuntime, slog.LevelInfo, "session_naming_scheduled", "session naming scheduled",
		"session_id", event.SessionID,
		"message_count", event.MessageCount,
		"trigger_step", event.TriggerStep,
	)
	return nil
}

func (n namingLogProjection) completed(ctx context.Context, event NamingCompletedEvent) error {
	emitNamingLog(ctx, event.TriggeredAt, events.LogRuntime, slog.LevelInfo, "session_naming_completed", "session naming completed",
		"session_id", event.SessionID,
		"title", event.Title,
		"provider", event.Provider,
		"model", event.Model,
		"message_count", event.MessageCount,
	)
	return nil
}

func (n namingLogProjection) failed(ctx context.Context, event NamingFailedEvent) error {
	level := slog.LevelError
	if errors.Is(event.Err, context.Canceled) && event.FallbackErr == nil {
		level = slog.LevelInfo
	}
	fields := []any{
		"session_id", event.SessionID,
		"stage", event.Stage,
		"provider", event.Provider,
		"model", event.Model,
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
		"fallback_error", event.FallbackErr,
	}
	emitNamingLog(ctx, event.TriggeredAt, events.LogRuntime, level, "session_naming_failed", "session naming failed", fields...)
	var diagnostic events.DiagnosticError
	if level == slog.LevelError && (event.Stage == "llm_error" || errors.As(event.Err, &diagnostic)) {
		emitNamingLog(ctx, event.TriggeredAt, events.LogAudit, level, "session_naming_failed", "session naming failed", fields...)
	}
	return nil
}

func emitNamingLog(ctx context.Context, at time.Time, category events.LogCategory, level slog.Level, eventName, summary string, attrs ...any) {
	record := events.LogRecord{At: at, Category: category, Level: level, Name: eventName, Module: "session", Summary: summary}
	detail := map[string]any{}
	for _, attr := range slog.Group("", attrs...).Value.Group() {
		if attr.Key == "generated_title_raw" || attr.Key == "generated_title_normalized" {
			detail[attr.Key] = attr.Value.Any()
			continue
		}
		if err, ok := attr.Value.Any().(error); ok {
			attr.Value = slog.StringValue(err.Error())
			var diagnostic events.DiagnosticError
			if errors.As(err, &diagnostic) {
				key := "upstream"
				if attr.Key == "fallback_error" {
					key = "fallback_upstream"
				}
				detail[key] = diagnostic.LogDiagnostic()
			}
		}
		record.Fields = append(record.Fields, attr)
	}
	if len(detail) > 0 {
		data, _ := json.Marshal(detail)
		record.Detail = string(data)
	}
	_ = events.EmitLog(ctx, record)
}

func (s *Service) connectLogSignals() {
	logs := namingLogProjection{}
	scheduled, _ := s.namingSignals.Scheduled.Connect(logs.scheduled, signal.ConnectOptions{})
	completed, _ := s.namingSignals.Completed.Connect(logs.completed, signal.ConnectOptions{})
	failed, _ := s.namingSignals.Failed.Connect(logs.failed, signal.ConnectOptions{})
	s.logConnections = []*signal.Connection{scheduled, completed, failed}
}
