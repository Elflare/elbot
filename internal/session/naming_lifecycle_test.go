package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/logging"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type namingGeneratorFunc func(context.Context, []storage.Message) (TitleResult, error)

type namingDiagnosticError struct{}

func (namingDiagnosticError) Error() string {
	return "upstream failed: gateway detail"
}

func TestNamingFailureContentSurvivesRuntimeLevel(t *testing.T) {
	for _, level := range []string{"info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			center, err := logging.NewManager(level, filepath.Join(t.TempDir(), "sessions.db"), 30)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = center.Close(context.Background()) })
			service := NewService(nil)
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
	service := NewService(nil)
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

func TestNamingFallbackFailurePreservesOriginalError(t *testing.T) {
	store := newTestStore(t)
	service := NewService(store)
	defer service.Close(context.Background())
	original := fmt.Errorf("upstream: %w", namingDiagnosticError{})
	event := NamingCompletedEvent{ResultStatus: events.ResultFailed, GenerationResultStatus: events.ResultFailed, GenerationError: original}
	service.handleNamingFailure(context.Background(), &storage.Session{ID: "missing"}, []storage.Message{{Role: storage.RoleUser, Content: "fallback text"}}, &event)
	if event.GenerationError != original || event.FallbackError == nil || event.FallbackResultStatus != events.ResultFailed || event.ResultStatus != events.ResultFailed {
		t.Fatalf("lost original failure: %+v", event)
	}
}

func (f namingGeneratorFunc) GenerateTitle(ctx context.Context, messages []storage.Message) (TitleResult, error) {
	return f(ctx, messages)
}

func namingRow(t *testing.T, svc *Service, store storage.Store) *storage.Session {
	t.Helper()
	row, err := svc.Create(context.Background(), Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, CreateRequest{Title: "original"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Messages().Append(context.Background(), &storage.Message{SessionID: row.ID, Role: storage.RoleUser, Content: "name this"}); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestNamingOutlivesTurnButStopsWithApplication(t *testing.T) {
	for _, resultError := range []bool{false, true} {
		t.Run(map[bool]string{false: "late success", true: "late failure"}[resultError], func(t *testing.T) {
			store := newTestStore(t)
			started, release := make(chan context.Context, 1), make(chan struct{})
			var unblock sync.Once
			gen := namingGeneratorFunc(func(ctx context.Context, _ []storage.Message) (TitleResult, error) {
				started <- ctx
				<-release
				if resultError {
					return TitleResult{}, errors.New("late model error")
				}
				return TitleResult{RawTitle: "late title"}, nil
			})
			notifier := &fakeNamingNotifier{failures: make(chan NamingCompletedEvent, 2)}
			svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, gen)
			notifier.connect(t, svc)
			appCtx, stopApp := context.WithCancel(context.Background())
			defer stopApp()
			svc.StartNaming(appCtx)
			t.Cleanup(func() { unblock.Do(func() { close(release) }); _ = svc.Close(context.Background()) })
			row := namingRow(t, svc, store)
			turnCtx, stopTurn := context.WithCancel(context.Background())
			model := contextinfo.Model{Provider: "deepseek", Model: "deepseek-chat", APIType: "chat"}
			turnCtx = contextinfo.WithModel(turnCtx, model)
			svc.MaybeScheduleNaming(turnCtx, row.ID)
			var workerCtx context.Context
			select {
			case workerCtx = <-started:
			case <-time.After(time.Second):
				t.Fatal("no worker")
			}
			if got, ok := contextinfo.ModelFromContext(workerCtx); !ok || got != model {
				t.Fatalf("worker model = %+v, present = %v", got, ok)
			}
			stopTurn()
			if workerCtx.Err() != nil {
				t.Fatal("Turn cancellation stopped naming")
			}
			stopApp()
			select {
			case <-workerCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("application did not cancel worker")
			}
			budget, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			if err := svc.Close(budget); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Close=%v", err)
			}
			select {
			case <-svc.Done():
				t.Fatal("reported exit before worker returned")
			default:
			}
			unblock.Do(func() { close(release) })
			if err := svc.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := svc.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			latest, err := store.Sessions().Get(context.Background(), row.ID)
			if err != nil || latest.Title != "original" {
				t.Fatalf("late title persisted: %#v/%v", latest, err)
			}
			select {
			case e := <-notifier.failures:
				want := events.ResultCanceled
				if resultError {
					want = events.ResultFailed
				}
				if e.ResultStatus != want || e.CancellationError == nil || !e.Scheduled || e.CompletedAt.IsZero() {
					t.Fatalf("lost terminal facts: %#v", e)
				}
			default:
				t.Fatal("shutdown discarded naming completion")
			}
			svc.StartNaming(context.Background())
			svc.MaybeScheduleNaming(context.Background(), row.ID)
			select {
			case <-started:
				t.Fatal("closed service restarted")
			default:
			}
		})
	}
}

func TestNamingStopRacesAdmission(t *testing.T) {
	store := newTestStore(t)
	var calls atomic.Int32
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, namingGeneratorFunc(func(ctx context.Context, _ []storage.Message) (TitleResult, error) {
		calls.Add(1)
		<-ctx.Done()
		return TitleResult{}, ctx.Err()
	}))
	svc.StartNaming(context.Background())
	row := namingRow(t, svc, store)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); svc.MaybeScheduleNaming(context.Background(), row.ID) }()
	}
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if calls.Load() > 1 {
		t.Fatalf("duplicate workers: %d", calls.Load())
	}
}

type namingPreparationRepo struct {
	storage.MessageRepository
	started chan struct{}
	release chan struct{}
}

func (r namingPreparationRepo) ListBySession(ctx context.Context, id string) ([]storage.Message, error) {
	close(r.started)
	<-ctx.Done()
	<-r.release
	return nil, ctx.Err()
}

type namingPreparationStore struct {
	storage.Store
	messages storage.MessageRepository
}

func (s namingPreparationStore) Messages() storage.MessageRepository { return s.messages }

func TestNamingCloseWaitsForPreparation(t *testing.T) {
	store := newTestStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	svc := NewServiceWithNaming(namingPreparationStore{store, namingPreparationRepo{store.Messages(), started, release}}, NamingConfig{TriggerStep: 1}, namingGeneratorFunc(func(context.Context, []storage.Message) (TitleResult, error) {
		t.Error("unexpected generator")
		return TitleResult{}, nil
	}))
	svc.StartNaming(context.Background())
	row := namingRow(t, svc, store)
	done := make(chan struct{})
	go func() { svc.MaybeScheduleNaming(context.Background(), row.ID); close(done) }()
	<-started
	budget, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := svc.Close(budget); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close=%v", err)
	}
	select {
	case <-svc.Done():
		t.Error("preparation still active")
	default:
	}
	close(release)
	<-done
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
