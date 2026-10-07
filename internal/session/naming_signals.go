package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/events"
	"elbot/internal/signal"
)

type namingLogProjection struct{}

func (n namingLogProjection) scheduled(ctx context.Context, event NamingScheduledEvent) error {
	return emitNamingLog(ctx, event.TriggeredAt, slog.LevelInfo, "session naming scheduled",
		"session_id", event.SessionID,
		"message_count", event.MessageCount,
		"trigger_step", event.TriggerStep,
	)
}

func (n namingLogProjection) completed(ctx context.Context, event NamingCompletedEvent) error {
	return emitNamingLog(ctx, event.TriggeredAt, slog.LevelInfo, "session naming completed",
		"session_id", event.SessionID,
		"title", event.Title,
		"provider", event.Provider,
		"model", event.Model,
		"message_count", event.MessageCount,
	)
}

func (n namingLogProjection) failed(ctx context.Context, event NamingFailedEvent) error {
	return emitNamingLog(ctx, event.TriggeredAt, slog.LevelWarn, "session naming failed",
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
	)
}

func emitNamingLog(ctx context.Context, at time.Time, level slog.Level, summary string, attrs ...any) error {
	record := events.LogRecord{At: at, Category: events.LogRuntime, Level: level, Name: strings.ReplaceAll(summary, " ", "_"), Module: "session", Summary: summary}
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
				detail["upstream"] = diagnostic.LogDiagnostic()
			}
		}
		record.Fields = append(record.Fields, attr)
	}
	if len(detail) > 0 {
		data, _ := json.Marshal(detail)
		record.Detail = string(data)
	}
	_ = events.EmitLog(ctx, record)
	return nil
}

func (s *Service) connectLogSignals() {
	logs := namingLogProjection{}
	scheduled, _ := s.namingSignals.Scheduled.Connect(logs.scheduled, signal.ConnectOptions{})
	completed, _ := s.namingSignals.Completed.Connect(logs.completed, signal.ConnectOptions{})
	failed, _ := s.namingSignals.Failed.Connect(logs.failed, signal.ConnectOptions{})
	s.logConnections = []*signal.Connection{scheduled, completed, failed}
}

type NamingSignals struct {
	Scheduled *signal.Signal[NamingScheduledEvent]
	Completed *signal.Signal[NamingCompletedEvent]
	Failed    *signal.Signal[NamingFailedEvent]
}

func newNamingSignals() NamingSignals {
	return NamingSignals{
		Scheduled: signal.New[NamingScheduledEvent]("session.naming_scheduled"),
		Completed: signal.New[NamingCompletedEvent]("session.naming_completed"),
		Failed:    signal.New[NamingFailedEvent]("session.naming_failed"),
	}
}

func (s *Service) NamingSignals() NamingSignals { return s.namingSignals }
