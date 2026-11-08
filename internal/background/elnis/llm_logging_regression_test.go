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

func (r joinedFailureBackgroundRunner) RunBackground(ctx context.Context, _ background.RunRequest) (context.Context, background.RunResult, error) {
	return ctx, background.RunResult{}, r.err
}

func TestElnisEventFailureDoesNotReclassifyDialogueError(t *testing.T) {
	failure := errors.New("independent model failure")
	service, cleanup := newTestServiceWithRunner(t, joinedFailureBackgroundRunner{err: failure}, nil)
	defer cleanup()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
	if _, err := service.Handle(t.Context(), "secret", testRequest(ModeLLM)); err != nil {
		t.Fatal(err)
	}
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.Module == "elnis" && (record.EventName == "elnis.llm_failed" || record.EventName == "elnis_llm_failed") {
			records = append(records, record)
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	if err := service.RunLLMEvent(t.Context(), queued.Event, queued.EventID); err != nil {
		t.Fatalf("handled model error escaped: %v", err)
	}
	row, err := testRepo(service).snapshot(t.Context(), queued.EventID)
	if err != nil || row.Status != StatusFailed || len(records) != 1 || records[0].ResultStatus != events.ResultFailed {
		t.Fatalf("event=%#v logs=%#v err=%v", row, records, err)
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
	service, cleanup := newTestServiceWithRunner(t, runner, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{}, nil
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
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.Category == events.LogCategoryElnis && record.EventName == "elnis_llm_failed" {
			records = append(records, record)
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	err = service.RunLLMEvent(context.Background(), queued.Event, queued.EventID)
	if err == nil || len(records) != 1 || records[0].Level != slog.LevelError || records[0].ResultStatus != events.ResultFailed {
		t.Fatalf("error=%v logs=%#v", err, records)
	}
	if !errors.Is(err, failure) || !strings.Contains(records[0].Content, failure.Error()) || !strings.Contains(records[0].Content, queued.EventID) || len(runner.requests) != 1 {
		t.Fatalf("save diagnostic or original error lost: %+v error=%v", records, err)
	}

}

func newElnisReportLoggingFixture(t *testing.T, send SenderFunc) (*Service, QueuedLLMEvent) {
	t.Helper()
	service, cleanup := newTestServiceWithRunner(t, &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"done"}`}, delivery.NewManager(elnisLoggingSender(send)).SendTo)
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

func TestElnisDeliveryErrorAndBusinessState(t *testing.T) {
	for _, mode := range []string{ModeDirect, ModeLLM} {
		for _, tc := range []struct {
			name   string
			status events.ResultStatus
			err    error
		}{
			{"success", events.ResultSucceeded, nil},
			{"wrapped_cancellation", events.ResultCanceled, fmt.Errorf("transport: %w", context.Canceled)},
			{"mixed_failure", events.ResultFailed, errors.New("independent transport failure")},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				send := func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
					return delivery.Receipt{PlatformMessageIDs: []string{"partial"}, SentMessages: []delivery.SentMessage{{Platform: "qqonebot", ChatType: "private", ChatID: "1001", PlatformMessageID: "partial", OutputIndexes: []int{0}}}}, tc.err
				}
				var service *Service
				var queued QueuedLLMEvent
				if mode == ModeLLM {
					service, queued = newElnisReportLoggingFixture(t, send)
				} else {
					var cleanup func()
					service, cleanup = newTestServiceWithRunner(t, nil, delivery.NewManager(elnisLoggingSender(send)).SendTo)
					t.Cleanup(cleanup)
				}
				var records []events.LogRecord
				connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
					if (record.Module == "elnis" || record.Module == "delivery") && (record.ResultStatus == events.ResultFailed || record.ResultStatus == events.ResultCanceled || record.EventName == "elnis.direct_completed" || record.EventName == "elnis.llm_completed") {
						records = append(records, record)
					}

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
				wantErr := tc.err
				if mode == ModeDirect {
					wantErr = nil
				}
				if err != wantErr {
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
				if err != nil || row.Status != wantState || len(records) != 1 {
					t.Fatalf("state=%#v error=%v records=%#v", row, err, records)
				}
				for _, record := range records {
					wantLevel := slog.LevelInfo
					if tc.status == events.ResultFailed {
						wantLevel = slog.LevelWarn
						if mode == ModeLLM {
							wantLevel = slog.LevelError
						}
					}
					if record.ResultStatus != tc.status || record.Level != wantLevel {
						t.Fatalf("source result reclassified: %#v", record)
					}
				}
			})
		}
	}
}

func TestElnisRecoverySuccessDoesNotEmitFailure(t *testing.T) {
	firstFailure := errors.New("first send failed")
	calls := 0
	service, queued := newElnisReportLoggingFixture(t, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		calls++
		if calls == 1 {
			return delivery.Receipt{}, firstFailure
		}
		return delivery.Receipt{}, nil
	})
	if err := service.RunLLMEvent(t.Context(), queued.Event, queued.EventID); err != firstFailure {
		t.Fatalf("initial error=%v", err)
	}
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.EventName == "recover_elnis_report_failed" {
			records = append(records, record)
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	service.recoverReports(t.Context())
	if len(records) != 0 {
		t.Fatal("recovery result= error= records=", err, records)
	}
	row, err := testRepo(service).snapshot(t.Context(), queued.EventID)
	if err != nil || row.Status != StatusCompleted || calls != 2 {
		t.Fatalf("business completion/retry policy changed: state=%#v error=%v sends=%d", row, err, calls)
	}
}

func TestElnisRecoveryFailureKeepsOneSourceDiagnostic(t *testing.T) {
	sendErr := errors.New("transport still offline")
	service, queued := newElnisReportLoggingFixture(t, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{}, sendErr
	})
	if err := service.RunLLMEvent(t.Context(), queued.Event, queued.EventID); err != sendErr {
		t.Fatalf("initial error=%v", err)
	}
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.ResultStatus == events.ResultFailed {
			records = append(records, record)
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	service.recoverReports(t.Context())
	if len(records) != 1 || records[0].EventName != "recover_elnis_report_failed" || records[0].Content != sendErr.Error() || records[0].Level != slog.LevelWarn {
		t.Fatalf("recovery repeated or reclassified source diagnostic: %#v", records)
	}
	row, err := testRepo(service).snapshot(t.Context(), queued.EventID)
	if err != nil || row.Status != StatusResultReady {
		t.Fatalf("retry state changed: state=%#v error=%v", row, err)
	}
}

func TestElnisReportPreservesSaveErrorAndRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		level   slog.Level
		saveErr error
	}{
		{"canceled_save", slog.LevelInfo, fmt.Errorf("database: %w", context.Canceled)},
		{"failed_save", slog.LevelError, errors.New("independent receipt save failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, queued := newElnisReportLoggingFixture(t, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
				return delivery.Receipt{PlatformMessageIDs: []string{"partial"}}, nil
			})
			service.store = failingReportReceiptRepository{EventRepository: service.store, err: tc.saveErr}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
				if record.EventName == "elnis_llm_failed" {
					records = append(records, record)
				}

			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			if err := service.RunLLMEvent(t.Context(), queued.Event, queued.EventID); !errors.Is(err, tc.saveErr) {
				t.Fatalf("business save error lost: %v", err)
			}
			if len(records) != 1 || records[0].Level != tc.level || !strings.Contains(records[0].Content, tc.saveErr.Error()) {
				t.Fatalf("independent failure or executed facts lost: %#v", records)
			}
			row, err := testRepo(service).snapshot(t.Context(), queued.EventID)
			if err != nil || row.Status != StatusResultReady {
				t.Fatalf("report retry state changed: %#v error=%v", row, err)
			}
		})
	}
}

func TestElnisFormatFailuresAreHandledBeforeRetryAndStateSave(t *testing.T) {
	runner := &fakeBackgroundRunner{text: "invalid json"}
	service, cleanup := newTestServiceWithRunner(t, runner, nil)
	defer cleanup()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
	if _, err := service.Handle(t.Context(), "secret", testRequest(ModeLLM)); err != nil {
		t.Fatal(err)
	}
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.EventName == "elnis_llm_failed" {
			records = append(records, record)
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	err = service.RunLLMEvent(t.Context(), queued.Event, queued.EventID)
	if err != nil || len(runner.requests) != 2 || len(records) != 2 || records[0].Level != slog.LevelWarn || records[0].ResultStatus != events.ResultFailed || records[1].Level != slog.LevelError {
		t.Fatalf("error=%v attempts=%d logs=%+v", err, len(runner.requests), records)
	}
}

type elnisLoggingSender SenderFunc

func (send elnisLoggingSender) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	return send(ctx, target, outputs)
}

type failingReportCleanupRepository struct {
	EventRepository
	err error
}

func (r failingReportCleanupRepository) MarkReportDeliveryFailed(context.Context, string, string, string, string, string, []background.ReportMessageMap) error {
	return r.err
}

func TestElnisSendAndCleanupFailuresEachLoggedOnce(t *testing.T) {
	sendErr, saveErr := errors.New("transport failed"), errors.New("failure state save failed")
	service, queued := newElnisReportLoggingFixture(t, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{PlatformMessageIDs: []string{"partial"}}, sendErr
	})
	service.store = failingReportCleanupRepository{EventRepository: service.store, err: saveErr}
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if strings.Contains(record.Content, sendErr.Error()) || strings.Contains(record.Content, saveErr.Error()) {
			records = append(records, record)
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	err = service.RunLLMEvent(t.Context(), queued.Event, queued.EventID)
	if !errors.Is(err, sendErr) || errors.Is(err, saveErr) {
		t.Fatalf("cleanup replaced or merged with primary failure: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("diagnostics duplicated or missing: %+v", records)
	}
	if records[0].EventName != "elnis_report_failure_save_failed" || records[0].Level != slog.LevelError || records[1].EventName != "elnis_llm_failed" || records[1].Level != slog.LevelError {
		t.Fatalf("incorrect owners or severity: %+v", records)
	}
	if strings.Contains(records[0].Content, sendErr.Error()) {
		t.Fatal("cleanup repeated transport error")
	}
}

func TestElnisNativeCallFailureOnlyReturns(t *testing.T) {
	service, cleanup := newTestServiceWithRunner(t, nil, nil)
	defer cleanup()
	failure := errors.New("native platform failed")
	service.callPlatformAPI = func(context.Context, string, string, map[string]any) (json.RawMessage, error) { return nil, failure }
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if strings.Contains(record.Content, failure.Error()) {
			records = append(records, record)
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	err = service.executeCalls(t.Context(), nil, []Call{{Platform: "telegram", API: "sendMessage"}})
	if err != failure || len(records) != 0 {
		t.Fatalf("native call diagnostic=%+v error=%v", records, err)
	}
}
