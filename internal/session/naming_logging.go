package session

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/events"
	"elbot/internal/signal"
)

type namingLogProjection struct{}

func (n namingLogProjection) scheduled(ctx context.Context, event NamingScheduledEvent) error {
	emitNamingLog(ctx, events.LogRecord{
		Category: events.LogRuntime, Level: slog.LevelInfo, EventName: "session_naming_scheduled", Module: "session",
		Identity: event.Identity,
		Content:  fmt.Sprintf("session naming scheduled: message_count=%d trigger_step=%d", event.MessageCount, event.TriggerStep),
	})
	return nil
}

func (n namingLogProjection) completed(ctx context.Context, event NamingCompletedEvent) error {
	level := slog.LevelInfo
	switch event.ResultStatus {
	case events.ResultFailed:
		level = slog.LevelError
	case events.ResultRejected:
		level = slog.LevelWarn
	case events.ResultSucceeded:
		if event.GenerationResultStatus == events.ResultFailed || event.TitleValid != nil && !*event.TitleValid {
			level = slog.LevelWarn
		}
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "session naming completed: result=%s provider=%s model=%s", event.ResultStatus, event.Provider, event.Model)
	if event.PreparationResultStatus != events.ResultUnspecified {
		fmt.Fprintf(&summary, "\npreparation %s: %s", event.PreparationOperation, event.PreparationResultStatus)
		if event.PreparationError != nil {
			fmt.Fprintf(&summary, "; error=%v", event.PreparationError)
		}
	}
	if event.GenerationResultStatus != events.ResultUnspecified {
		fmt.Fprintf(&summary, "\ngeneration: %s", event.GenerationResultStatus)
		if event.GenerationError != nil {
			fmt.Fprintf(&summary, "; error=%v", event.GenerationError)
		}
	}
	if event.TitleValid != nil && !*event.TitleValid {
		fmt.Fprintf(&summary, "\ninvalid title: %s", event.InvalidReason)
	}
	if event.PersistenceResultStatus != events.ResultUnspecified {
		fmt.Fprintf(&summary, "\npersistence: %s", event.PersistenceResultStatus)
		if event.PersistenceError != nil {
			fmt.Fprintf(&summary, "; error=%v", event.PersistenceError)
		}
	}
	if event.FallbackResultStatus != events.ResultUnspecified {
		fmt.Fprintf(&summary, "\nfallback: %s title=%q", event.FallbackResultStatus, event.FallbackTitle)
		if event.FallbackError != nil {
			fmt.Fprintf(&summary, "; error=%v", event.FallbackError)
		}
	}
	if event.CancellationError != nil {
		fmt.Fprintf(&summary, "\ncancellation: %v", event.CancellationError)
	}
	if event.Title != nil {
		fmt.Fprintf(&summary, "\nactual title=%q", *event.Title)
	}
	if event.MessageCount != nil {
		fmt.Fprintf(&summary, "\nmessage_count=%d", *event.MessageCount)
	}
	fmt.Fprintf(&summary, " failures=%d/%d", event.FailureCount, event.MaxFailures)
	record := events.LogRecord{
		Category: events.LogRuntime, Level: level, EventName: "session_naming_completed", Module: "session",
		Content: summary.String(), ContentLimit: 4096, Identity: event.Identity, ResultStatus: event.ResultStatus,
	}
	emitNamingLog(ctx, record)
	// A terminal generator failure remains auditable even when a fallback title
	// recovers the flow or runtime filtering suppresses its degradation warning.
	if event.GenerationResultStatus == events.ResultFailed {
		record.Category = events.LogAudit
		emitNamingLog(ctx, record)
	}
	return nil
}

func emitNamingLog(ctx context.Context, record events.LogRecord) {
	_ = events.EmitLog(ctx, record)
}

func (s *Service) connectLogSignals() {
	logs := namingLogProjection{}
	scheduled, _ := s.namingSignals.Scheduled.Observe(logs.scheduled, signal.ConnectOptions{})
	completed, _ := s.namingSignals.Completed.Observe(logs.completed, signal.ConnectOptions{})
	s.logConnections = []*signal.Connection{scheduled, completed}
}
