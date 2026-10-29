package naming

import (
	"bytes"
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type namingDiagnosticError struct{}

func (namingDiagnosticError) Error() string {
	return "upstream failed: gateway detail"
}

func captureNamingDiagnostics(t *testing.T) *os.File {
	t.Helper()
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	previousStderr, previousLogger := os.Stderr, slog.Default()
	os.Stderr = file
	slog.SetDefault(slog.New(slog.NewTextHandler(file, nil)))
	t.Cleanup(func() {
		if err := signal.FlushDiagnostics(context.Background()); err != nil {
			t.Error(err)
		}
		os.Stderr = previousStderr
		slog.SetDefault(previousLogger)
		_ = file.Close()
	})
	return file
}

func TestNamingDeliveryCallbackErrorsReportedOnce(t *testing.T) {
	for _, source := range []string{"log", "naming"} {
		for _, stage := range []string{"scheduled", "completed", "audit"} {
			if source == "naming" && stage == "audit" {
				continue
			}
			t.Run(source+"/"+stage, func(t *testing.T) {
				file := captureNamingDiagnostics(t)
				s, err := NewService(Config{}, ServiceDeps{})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close(context.Background())
				failure := errors.New("delivery observer failed")
				var connection *signal.Connection
				if source == "log" {
					connection, err = events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
						if stage == "audit" && record.Category != events.LogAudit {
							return nil
						}
						return failure
					}, signal.ConnectOptions{})
				} else if stage == "scheduled" {
					connection, err = s.namingSignals.Scheduled.Observe(func(context.Context, NamingScheduledEvent) error { return failure }, signal.ConnectOptions{})
				} else {
					connection, err = s.namingSignals.Completed.Observe(func(context.Context, NamingCompletedEvent) error { return failure }, signal.ConnectOptions{})
				}
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Close(context.Background())
				id := "source"
				identity := events.LogIdentity{SessionID: &id}
				if stage == "scheduled" {
					s.notifyNamingScheduled(t.Context(), NamingScheduledEvent{Identity: identity})
				} else {
					event := NamingCompletedEvent{Identity: identity, ResultStatus: events.ResultSucceeded}
					if stage == "audit" {
						event.GenerationResultStatus = events.ResultFailed
					}
					s.notifyNamingCompleted(t.Context(), event)
				}
				if err := s.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := signal.FlushDiagnostics(t.Context()); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(file.Name())
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(data), failure.Error()) != 1 {
					t.Fatalf("expected one diagnostic: %s", data)
				}
			})
		}
	}
}

func TestNamingDeliveryFailureSummaryIDs(t *testing.T) {
	for _, stage := range []string{"scheduled", "completed"} {
		for _, failure := range []string{"queue_full", "closed", "timeout"} {
			for _, fullIdentity := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/full=%v", stage, failure, fullIdentity), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						var logs bytes.Buffer
						previous := slog.Default()
						slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
						defer slog.SetDefault(previous)
						s := &Service{namingSignals: namingSignals{
							Scheduled: signal.New[NamingScheduledEvent, signal.Empty]("scheduled", signal.Options[NamingScheduledEvent]{Concurrency: 1, Capacity: 1, Timeout: time.Second}),
							Completed: signal.New[NamingCompletedEvent, signal.Empty]("completed", signal.Options[NamingCompletedEvent]{Concurrency: 1, Capacity: 1, Timeout: time.Second}),
						}}
						defer s.namingSignals.Scheduled.Close(context.Background())
						defer s.namingSignals.Completed.Close(context.Background())
						release := make(chan struct{})
						unblock := sync.OnceFunc(func() { close(release) })
						defer unblock()
						var seed func() error
						var closeSignal func(context.Context) error
						var err error
						if stage == "scheduled" {
							_, err = s.namingSignals.Scheduled.Observe(func(context.Context, NamingScheduledEvent) error { <-release; return nil }, signal.ConnectOptions{})
							seed = func() error {
								_, err := s.namingSignals.Scheduled.EmitAsync(context.Background(), NamingScheduledEvent{})
								return err
							}
							closeSignal = s.namingSignals.Scheduled.Close
						} else {
							_, err = s.namingSignals.Completed.Observe(func(context.Context, NamingCompletedEvent) error { <-release; return nil }, signal.ConnectOptions{})
							seed = func() error {
								_, err := s.namingSignals.Completed.EmitAsync(context.Background(), NamingCompletedEvent{})
								return err
							}
							closeSignal = s.namingSignals.Completed.Close
						}
						if err != nil {
							t.Fatal(err)
						}
						wantError := context.DeadlineExceeded.Error()
						if failure == "closed" {
							if err := closeSignal(t.Context()); err != nil {
								t.Fatal(err)
							}
							wantError = signal.ErrClosed.Error()
						} else if failure == "queue_full" {
							for range 2 {
								if err := seed(); err != nil {
									t.Fatal(err)
								}
								synctest.Wait()
							}
							wantError = signal.ErrQueueFull.Error()
						}
						id, request, run, attempt, root := "source", "request", "run", "attempt", "root"
						identity := events.LogIdentity{SessionID: &id}
						if fullIdentity {
							identity.RequestID = &request
							identity.RunID = &run
							identity.Attempt = &attempt
							identity.RootRequestID = &root
						}
						if stage == "scheduled" {
							s.notifyNamingScheduled(t.Context(), NamingScheduledEvent{Identity: identity})
						} else {
							s.notifyNamingCompleted(t.Context(), NamingCompletedEvent{Identity: identity})
						}
						unblock()
						synctest.Wait()
						got := logs.String()
						if strings.Count(got, "delivery failed") != 1 || !strings.Contains(got, wantError) || !strings.Contains(got, "session_id=source") || strings.Contains(got, "0x") || strings.Contains(got, "identity=") {
							t.Fatalf("bad diagnostic: %s", got)
						}
						for _, field := range []string{"request_id=request", "run_id=run", "attempt=attempt", "root_request_id=root"} {
							key := strings.SplitN(field, "=", 2)[0] + "="
							if fullIdentity && !strings.Contains(got, field) || !fullIdentity && strings.Contains(got, key) {
								t.Fatalf("wrong optional identity %s: %s", field, got)
							}
						}
					})
				})
			}
		}
	}
}

func TestNamingLogFailureDoesNotChangeBusinessResult(t *testing.T) {
	file := captureNamingDiagnostics(t)
	connection, err := events.LogSubmitted.Observe(func(context.Context, events.LogRecord) error { return errors.New("log observer unavailable") }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
		return llm.TextResult{Text: "saved title"}, nil
	})
	id := f.row(t, storage.Message{Role: storage.RoleUser, Content: "user"})
	event := f.attempt(t, id)
	if event.ResultStatus != events.ResultSucceeded || event.Title == nil || *event.Title != "saved title" {
		t.Fatalf("log failure changed result: %+v", event)
	}
	row, err := f.store.Sessions().Get(t.Context(), id)
	if err != nil || row.Title != "saved title" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "log observer unavailable") != 2 {
		t.Fatalf("expected one report for each of two log submissions: %s", data)
	}
}

func TestNamingFailureContentSurvivesRuntimeLevel(t *testing.T) {
	for _, level := range []string{"info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			center, err := logging.NewManager(level, filepath.Join(t.TempDir(), "sessions.db"), 30)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = center.Close(context.Background()) })
			service, err := NewService(Config{}, ServiceDeps{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Close(context.Background()) })
			at := time.Now().Add(-time.Minute).Truncate(time.Second)
			sessionID, requestID := "source", "naming-request"
			ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "wrong", RequestID: "wrong"})
			service.notifyNamingCompleted(ctx, NamingCompletedEvent{
				Identity:    events.LogIdentity{SessionID: &sessionID, RequestID: &requestID},
				TriggeredAt: at, CompletedAt: time.Now(), Provider: "provider", Model: "model",
				ResultStatus: events.ResultFailed, GenerationResultStatus: events.ResultFailed,
				GenerationError: fmt.Errorf("wrapped: %w", namingDiagnosticError{}),
			})
			if err := center.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"audit", "elbot"} {
				paths, err := filepath.Glob(filepath.Join(center.LogDir(), prefix+"-*.log"))
				if err != nil || len(paths) != 1 {
					t.Fatalf("paths=%v err=%v", paths, err)
				}
				data, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				var entry struct {
					Time                                               time.Time
					Level, Content, SessionID, RequestID, ResultStatus string
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(data, &raw); err != nil {
					t.Fatal(err)
				}
				for key, target := range map[string]any{"time": &entry.Time, "level": &entry.Level, "content": &entry.Content, "session_id": &entry.SessionID, "request_id": &entry.RequestID, "result_status": &entry.ResultStatus} {
					if err := json.Unmarshal(raw[key], target); err != nil {
						t.Fatal(err)
					}
				}
				if entry.Level != "ERROR" || !entry.Time.After(at) || entry.SessionID != sessionID || entry.RequestID != requestID || entry.ResultStatus != "failed" {
					t.Fatalf("lost facts: %+v", entry)
				}
				if !strings.Contains(entry.Content, "wrapped: "+(namingDiagnosticError{}).Error()) {
					t.Fatalf("source diagnostic changed: %s", data)
				}
			}
		})
	}
}

func TestNamingOwnsSynchronousLogProjection(t *testing.T) {
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error { records = append(records, r); return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	service, err := NewService(Config{}, ServiceDeps{})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(123, 0)
	sessionID, title := "source", "title"
	ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "wrong", RequestID: "request"})
	service.notifyNamingCompleted(ctx, NamingCompletedEvent{Identity: events.LogIdentity{SessionID: &sessionID}, TriggeredAt: at, Title: &title, ResultStatus: events.ResultSucceeded})
	if len(records) != 1 || records[0].EventName != "session_naming_completed" || records[0].Identity.SessionID == nil || *records[0].Identity.SessionID != "source" || records[0].Identity.RequestID != nil {
		t.Fatal(records)
	}
	sessionID = "changed"
	if *records[0].Identity.SessionID != "source" {
		t.Fatal("identity aliased source")
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.notifyNamingCompleted(ctx, NamingCompletedEvent{ResultStatus: events.ResultSucceeded})
	if len(records) != 1 {
		t.Fatal("naming log connection survived close")
	}
}
