package pipeline

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"errors"
	"fmt"
	"log/slog"
	"testing"
)

func TestManagerLogsOperationErrors(t *testing.T) {
	fault := errors.New("plugin failed")
	for _, tc := range []struct {
		name   string
		err    error
		status events.ResultStatus
		level  slog.Level
	}{
		{"success", nil, events.ResultSucceeded, slog.LevelInfo},
		{"failure", fault, events.ResultFailed, slog.LevelWarn},
		{"cancel", context.Canceled, events.ResultCanceled, slog.LevelInfo},
		{"wrapped_cancel", fmt.Errorf("plugin: %w", context.Canceled), events.ResultCanceled, slog.LevelInfo},
		{"joined_cancel", errors.Join(context.Canceled, fmt.Errorf("plugin: %w", context.Canceled)), events.ResultCanceled, slog.LevelInfo},
		{"timeout", context.DeadlineExceeded, events.ResultFailed, slog.LevelWarn},
		{"mixed", errors.Join(fault, context.Canceled), events.ResultFailed, slog.LevelWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var records []events.LogRecord
			manager := NewManager(func(_ context.Context, r events.LogRecord) { records = append(records, r) }, nil)
			manager.Register(share.Registration{Name: "source", Point: share.PointAgentInputPrepared, Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, error) { return event, tc.err })})
			_, err := manager.Run(t.Context(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true})
			if !errors.Is(err, tc.err) || len(records) != 1 || records[0].ResultStatus != tc.status || records[0].Level != tc.level {
				t.Fatalf("err=%v records=%+v", err, records)
			}
		})
	}
}
