// 验证 Hook 日志发布和流水线执行结果的来源语义。

package hook

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestLogHookPreservesSourceRecordAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "context-session"}))
	cancel()
	identity := ""
	record := events.LogRecord{Category: events.LogAudit, Level: slog.LevelInfo, EventName: "hook_tool_call_completed", Module: "hook", Type: events.ContentJSON, Content: `{"result":"来源正文"}`, ContentLimit: 0, Identity: events.LogIdentity{SessionID: &identity}, ResultStatus: events.ResultFailed}
	var received events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(ctx context.Context, value events.LogRecord) error {
		if ctx.Err() != nil {
			t.Fatal("publication inherited cancellation")
		}
		received = value
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	LogHook(ctx, record)
	identity = "changed"
	if received.Content != record.Content || received.Level != slog.LevelInfo || received.ResultStatus != events.ResultFailed || received.Identity.SessionID == nil || *received.Identity.SessionID != "" || received.Identity.RunID != nil {
		t.Fatalf("source record changed: %+v", received)
	}
}

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
			manager.Register(Registration{Name: "source", Point: PointAgentInputPrepared, Match: Always(), Handler: HandlerFunc(func(_ context.Context, event Event) (Event, events.LogResult, error) { return event, declared, tc.err })})
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error { records = append(records, record); return nil }, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			_, outcome, err := manager.Run(t.Context(), Event{Point: PointAgentInputPrepared})
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
			manager.Register(Registration{Name: outcome.Content, Point: PointAgentInputPrepared, Priority: i, Match: Always(), Handler: HandlerFunc(func(_ context.Context, event Event) (Event, events.LogResult, error) { return event, outcome, nil })})
		}
		var outcome events.LogResult
		var err error
		if notify {
			outcome, err = manager.Notify(t.Context(), Event{Point: PointAgentInputPrepared})
		} else {
			_, outcome, err = manager.Run(t.Context(), Event{Point: PointAgentInputPrepared})
		}
		if err != nil || outcome.ResultStatus != events.ResultFailed || !strings.Contains(outcome.Content, "canceled stage") || !strings.Contains(outcome.Content, "independent failure") || !strings.Contains(outcome.Content, "completed stage") {
			t.Fatalf("notify=%v outcome=%+v err=%v", notify, outcome, err)
		}
	}
}
