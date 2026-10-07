package modelmgr

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/events"
	"elbot/internal/signal"
)

func (s *Service) connectLogSignals() error {
	connection, err := s.retrying.Connect(logModelRetry, signal.ConnectOptions{})
	if err != nil {
		return err
	}
	s.logConnections = append(s.logConnections, connection)
	return nil
}
func logModelRetry(ctx context.Context, event ModelRetryingEvent) error {
	errorMessage, detail := "", ""
	if event.Retry.Err != nil {
		errorMessage = event.Retry.Err.Error()
	}
	var diagnostic events.DiagnosticError
	if errors.As(event.Retry.Err, &diagnostic) {
		detail = diagnostic.LogDiagnostic().Detail
	}
	level := slog.LevelWarn
	if errors.Is(event.Retry.Err, context.Canceled) {
		level = slog.LevelInfo
	}
	_ = events.EmitLog(ctx, events.LogRecord{Category: events.LogRuntime, Level: level, Detail: detail, Name: "model_retry", Module: "model", Summary: "model call retry", Fields: []slog.Attr{
		slog.String("provider", event.Provider), slog.Int("retry_attempt", event.Retry.Attempt), slog.Int("max_retries", event.Retry.MaxRetries), slog.Duration("delay", event.Retry.Delay), slog.String("error", errorMessage),
	}})
	return nil
}
