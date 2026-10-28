package modelmgr

import (
	"context"
	"fmt"
	"log/slog"

	"elbot/internal/events"
	"elbot/internal/signal"
)

func (s *ModelManager) connectLogSignals() error {
	connection, err := s.retrying.Observe(logModelRetry, signal.ConnectOptions{})
	if err != nil {
		return err
	}
	s.logConnections = append(s.logConnections, connection)
	return nil
}
func logModelRetry(ctx context.Context, event ModelRetryingEvent) error {
	errorMessage := ""
	if event.Retry.Err != nil {
		errorMessage = event.Retry.Err.Error()
	}
	level := slog.LevelWarn
	if event.Retry.ResultStatus == events.ResultCanceled {
		level = slog.LevelInfo
	}
	_ = events.EmitLog(ctx, events.LogRecord{
		Category: events.LogRuntime, Level: level, EventName: "model_retry", Module: "model",
		Identity: event.Identity, ResultStatus: event.Retry.ResultStatus,
		Content: fmt.Sprintf("model call retry: provider=%s retry_attempt=%d max_retries=%d delay=%s error=%s", event.Provider, event.Retry.Attempt, event.Retry.MaxRetries, event.Retry.Delay, errorMessage),
	})
	return nil
}
