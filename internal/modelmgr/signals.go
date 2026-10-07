package modelmgr

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

// ModelRetryingEvent is emitted with the actual provider call's context. That
// context expires when the response stream ends, even if its parent survives.
type ModelRetryingEvent struct {
	Provider string
	Retry    llm.RetryEvent
}

func (s *Service) ModelRetrying() *signal.Signal[ModelRetryingEvent] { return s.retrying }

func (s *Service) connectLogSignals() error {
	connection, err := s.retrying.Connect(logModelRetry, signal.ConnectOptions{})
	if err != nil {
		return err
	}
	s.logConnections = append(s.logConnections, connection)
	return nil
}
func logModelRetry(ctx context.Context, event ModelRetryingEvent) error {
	message, detail := "", ""
	if event.Retry.Err != nil {
		message = event.Retry.Err.Error()
	}
	var diagnostic events.DiagnosticError
	if errors.As(event.Retry.Err, &diagnostic) {
		detail = diagnostic.LogDiagnostic().Detail
	}
	level := slog.LevelWarn
	if event.Retry.Err == context.Canceled {
		level = slog.LevelInfo
	}
	_ = events.EmitLog(ctx, events.LogRecord{Category: events.LogRuntime, Level: level, Detail: detail, Name: "model_retry", Module: "model", Summary: "model call retry", Fields: []slog.Attr{
		slog.String("provider", event.Provider), slog.Int("retry_attempt", event.Retry.Attempt), slog.Int("max_retries", event.Retry.MaxRetries), slog.Duration("delay", event.Retry.Delay), slog.String("error", message),
	}})
	return nil
}

// Close releases only this instance's synchronous projections.
func (s *Service) Close() error {
	for _, connection := range s.logConnections {
		connection.Disconnect()
	}
	return nil
}
