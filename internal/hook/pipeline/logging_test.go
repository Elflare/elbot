package pipeline

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/signal"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestManagerUsesDeclaredOutcomeIndependentlyOfBusinessError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status events.ResultStatus
		err    error
		level  slog.Level
	}{
		{"failed_without_error", events.ResultFailed, nil, slog.LevelError},
		{"failed_with_cancellation", events.ResultFailed, context.Canceled, slog.LevelError},
		{"succeeded_with_business_error", events.ResultSucceeded, errors.New("business only"), slog.LevelInfo},
		{"canceled_without_error", events.ResultCanceled, nil, slog.LevelInfo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := newTestManager()

			declared := events.LogResult{ResultStatus: tc.status, Content: "explicit source facts"}
			manager.Register(share.Registration{Name: "source", Point: share.PointAgentInputPrepared, Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
				return event, declared, tc.err
			})})
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error { records = append(records, record); return nil }, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			_, outcome, err := manager.Run(t.Context(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true})
			if !errors.Is(err, tc.err) || outcome != declared || len(records) != 1 || records[0].ResultStatus != tc.status || records[0].Level != tc.level || !strings.Contains(records[0].Content, declared.Content) {
				t.Fatalf("outcome=%+v err=%v records=%+v", outcome, err, records)
			}
		})
	}
}

func TestManagerRetainsFailureAcrossSamePointHandlers(t *testing.T) {
	for _, notify := range []bool{false, true} {
		manager := newTestManager()

		for i, outcome := range []events.LogResult{{ResultStatus: events.ResultCanceled, Content: "canceled stage"}, {ResultStatus: events.ResultFailed, Content: "independent failure"}, {ResultStatus: events.ResultSucceeded, Content: "completed stage"}} {
			manager.Register(share.Registration{Name: outcome.Content, Point: share.PointAgentInputPrepared, Priority: i, Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
				return event, outcome, nil
			})})
		}
		var outcome events.LogResult
		var err error
		if notify {
			outcome, err = manager.Notify(t.Context(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true})
		} else {
			_, outcome, err = manager.Run(t.Context(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true})
		}
		if err != nil || outcome.ResultStatus != events.ResultFailed || !strings.Contains(outcome.Content, "canceled stage") || !strings.Contains(outcome.Content, "independent failure") || !strings.Contains(outcome.Content, "completed stage") {
			t.Fatalf("notify=%v outcome=%+v err=%v", notify, outcome, err)
		}
	}
}
