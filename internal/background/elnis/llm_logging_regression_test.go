package elnis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	background "elbot/internal/background/share"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

type failCompletedEventRepository struct {
	EventRepository
	err error
}

type joinedFailureBackgroundRunner struct{ err error }

type failingReportReceiptRepository struct {
	EventRepository
	err error
}

func (r failingReportReceiptRepository) MarkReportDeliveriesDelivered(context.Context, []string, string, []background.ReportMessageMap) error {
	return r.err
}

func (r joinedFailureBackgroundRunner) RunBackground(context.Context, background.RunRequest) (background.RunResult, error) {
	return background.RunResult{}, r.err
}

func TestElnisEventFailureDoesNotReclassifyDialogueError(t *testing.T) {
	failure := errors.Join(context.Canceled, errors.New("independent model failure"))
	service, cleanup := newTestServiceWithRunner(t, joinedFailureBackgroundRunner{err: failure}, nil)
	defer cleanup()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
	if _, err := service.Handle(t.Context(), "secret", testRequest(ModeLLM)); err != nil {
		t.Fatal(err)
	}
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.Module == "elnis" && (record.EventName == "elnis.llm_failed" || record.EventName == "elnis_llm_failed") {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	if err := service.RunLLMEvent(t.Context(), queued.Event, queued.EventID); err != failure {
		t.Fatalf("business error changed: %v", err)
	}
	row, err := testRepo(service).snapshot(t.Context(), queued.EventID)
	if err != nil || row.Status != StatusFailed || len(records) != 2 {
		t.Fatalf("event=%#v logs=%#v err=%v", row, records, err)
	}
	for _, record := range records {
		if record.ResultStatus != events.ResultFailed || !strings.Contains(record.Content, "independent model failure") || !strings.Contains(record.Content, "context canceled") {
			t.Fatalf("event result reconstructed from dialogue error: %#v", record)
		}
	}
}

func (r failCompletedEventRepository) Update(ctx context.Context, req UpdateEventRequest) error {
	if req.Status == StatusCompleted {
		return r.err
	}
	return r.EventRepository.Update(ctx, req)
}

func TestElnisLogsSuccessfulRunSaveFailure(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":false,"report":"done"}`}
	service, cleanup := newTestServiceWithRunner(t, runner, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})
	defer cleanup()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
	req := testRequest(ModeLLM)
	if _, err := service.Handle(context.Background(), "secret", req); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("completion state save failed")
	service.store = failCompletedEventRepository{EventRepository: service.store, err: failure}

	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.Category == events.LogElnis && record.EventName == "elnis_llm_failed" {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	err = service.RunLLMEvent(context.Background(), queued.Event, queued.EventID)
	if err == nil || len(records) != 1 || records[0].ResultStatus != events.ResultFailed {
		t.Fatalf("error=%v logs=%#v", err, records)
	}
	var content map[string]json.RawMessage
	if err := json.Unmarshal([]byte(records[0].Content), &content); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"elwisp_name": req.Elwisp.Name, "source": req.Source, "source_id": req.ID, "mode": req.Mode, "event_id": queued.EventID} {
		var got string
		if err := json.Unmarshal(content[key], &got); err != nil || got != want {
			t.Fatalf("%s=%s error=%v", key, content[key], err)
		}
	}
	if string(content["tags"]) != `["test"]` || len(content["event_key"]) == 0 {
		t.Fatalf("query facts lost: %s", records[0].Content)
	}
	if !errors.Is(err, failure) || !strings.Contains(string(content["persistence_error"]), failure.Error()) || !strings.Contains(string(content["result"]), `"completed":true`) || len(runner.requests) != 1 {
		t.Fatalf("completed facts lost: %s error=%v", records[0].Content, err)
	}

}

func newElnisReportLoggingFixture(t *testing.T, send SenderFunc) (*Service, QueuedLLMEvent) {
	t.Helper()
	service, cleanup := newTestServiceWithRunner(t, &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"done"}`}, send)
	t.Cleanup(cleanup)
	ctx := t.Context()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
	req := testRequest(ModeLLM)
	req.Targets = []Target{{Platform: "qqonebot", Type: "private", ID: "1001"}}
	if _, err := service.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	return service, queued
}

func TestElnisDeliveryPreservesSourceLogResultAndBusinessState(t *testing.T) {
	for _, mode := range []string{ModeDirect, ModeLLM} {
		for _, tc := range []struct {
			name   string
			status events.ResultStatus
			err    error
		}{
			{"failed_without_business_error", events.ResultFailed, nil},
			{"wrapped_cancellation", events.ResultCanceled, fmt.Errorf("transport: %w", context.Canceled)},
			{"mixed_failure", events.ResultFailed, errors.Join(context.Canceled, errors.New("independent transport failure"))},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				send := func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
					return delivery.Receipt{PlatformMessageIDs: []string{"partial"}, SentMessages: []delivery.SentMessage{{Platform: "qqonebot", ScopeID: "private:1001", PlatformMessageID: "partial", OutputIndexes: []int{0}}}}, events.LogResult{ResultStatus: tc.status, Content: "source diagnostic; one destination received output"}, tc.err
				}
				var service *Service
				var queued QueuedLLMEvent
				if mode == ModeLLM {
					service, queued = newElnisReportLoggingFixture(t, send)
				} else {
					var cleanup func()
					service, cleanup = newTestServiceWithRunner(t, nil, send)
					t.Cleanup(cleanup)
				}
				var records []events.LogRecord
				connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
					if record.Module == "elnis" && strings.Contains(record.Content, "source diagnostic") {
						records = append(records, record)
					}
					return nil
				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(connection.Disconnect)
				var eventID string
				if mode == ModeLLM {
					err = service.RunLLMEvent(t.Context(), queued.Event, queued.EventID)
					eventID = queued.EventID
				} else {
					req := testRequest(ModeDirect)
					req.Targets = []Target{{Platform: "qqonebot", Type: "private", ID: "1001"}}
					response, runErr := service.Handle(t.Context(), "secret", req)
					err = runErr
					if tc.err == nil && (!response.Accepted || response.Status != StatusCompleted) {
						t.Fatalf("business response changed: %#v", response)
					}
					row, getErr := testRepo(service).snapshotByKey(t.Context(), req.Elwisp.Name, req.Source, req.ID)
					if getErr != nil {
						t.Fatal(getErr)
					}
					eventID = row.ID
				}
				if err != tc.err {
					t.Fatalf("business error changed: got=%v want=%v", err, tc.err)
				}
				row, err := testRepo(service).snapshot(t.Context(), eventID)
				wantState := StatusCompleted
				if tc.err != nil {
					wantState = StatusFailed
					if mode == ModeLLM {
						wantState = StatusResultReady
					}
				}
				if err != nil || row.Status != wantState || len(records) == 0 {
					t.Fatalf("state=%#v error=%v records=%#v", row, err, records)
				}
				for _, record := range records {
					if record.ResultStatus != tc.status || (tc.status == events.ResultCanceled && record.Level != slog.LevelInfo) {
						t.Fatalf("source result reclassified: %#v", record)
					}
				}
			})
		}
	}
}

func TestElnisRecoveryKeepsDeclaredFailureWithNilBusinessError(t *testing.T) {
	firstFailure := errors.New("first send failed")
	calls := 0
	service, queued := newElnisReportLoggingFixture(t, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		calls++
		if calls == 1 {
			return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultFailed, Content: firstFailure.Error()}, firstFailure
		}
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultFailed, Content: "recovery partially delivered"}, nil
	})
	if err := service.RunLLMEvent(t.Context(), queued.Event, queued.EventID); err != firstFailure {
		t.Fatalf("initial error=%v", err)
	}
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "recover_elnis_report_failed" {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	logResult, err := service.recoverReports(t.Context())
	if err != nil || logResult.ResultStatus != events.ResultFailed || len(records) != 1 || records[0].ResultStatus != events.ResultFailed || !strings.Contains(records[0].Content, "recovery partially delivered") {
		t.Fatalf("recovery result=%#v error=%v records=%#v", logResult, err, records)
	}
	row, err := testRepo(service).snapshot(t.Context(), queued.EventID)
	if err != nil || row.Status != StatusCompleted || calls != 2 {
		t.Fatalf("business completion/retry policy changed: state=%#v error=%v sends=%d", row, err, calls)
	}
}

func TestElnisReportPreservesIndependentFailureAcrossCancellation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sendStatus events.ResultStatus
		saveErr    error
	}{
		{"failed_send_then_canceled_save", events.ResultFailed, fmt.Errorf("database: %w", context.Canceled)},
		{"canceled_send_then_failed_save", events.ResultCanceled, errors.New("independent receipt save failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, queued := newElnisReportLoggingFixture(t, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				return delivery.Receipt{PlatformMessageIDs: []string{"partial"}}, events.LogResult{ResultStatus: tc.sendStatus, Content: "declared send diagnostic"}, nil
			})
			service.store = failingReportReceiptRepository{EventRepository: service.store, err: tc.saveErr}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "elnis_llm_report_delivery_failed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			if err := service.RunLLMEvent(t.Context(), queued.Event, queued.EventID); !errors.Is(err, tc.saveErr) {
				t.Fatalf("business save error lost: %v", err)
			}
			if len(records) != 1 || records[0].ResultStatus != events.ResultFailed || !strings.Contains(records[0].Content, "declared send diagnostic") || !strings.Contains(records[0].Content, tc.saveErr.Error()) {
				t.Fatalf("independent failure or executed facts lost: %#v", records)
			}
			row, err := testRepo(service).snapshot(t.Context(), queued.EventID)
			if err != nil || row.Status != StatusResultReady {
				t.Fatalf("report retry state changed: %#v error=%v", row, err)
			}
		})
	}
}
