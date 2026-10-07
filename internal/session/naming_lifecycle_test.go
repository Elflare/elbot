package session

import (
	"context"
	"errors"
	"fmt"
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

func (namingDiagnosticError) Error() string { return "upstream failed" }
func (namingDiagnosticError) LogDiagnostic() events.LogDiagnostic {
	return events.LogDiagnostic{Kind: "response_error", Detail: `{"description":"gateway detail","api_key":"secret-credential","payload":"` + strings.Repeat("x", 10000) + `"}`}
}

func TestNamingFailureDiagnosticSurvivesRuntimeLevel(t *testing.T) {
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
			ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "wrong", RequestID: "naming-request", RunID: "naming-run", Attempt: "attempt", RootRequestID: "root"})
			service.notifyNamingFailed(ctx, NamingFailedEvent{SessionID: "source", TriggeredAt: at, Stage: "llm_error", Provider: "provider", Model: "model", Err: fmt.Errorf("wrapped: %w", namingDiagnosticError{})})
			if err := center.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"audit", "elbot"} {
				entries, err := (logging.Reader{Dir: center.LogDir()}).Query(context.Background(), logging.LogQuery{Prefix: prefix, Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 {
					t.Fatalf("%s: got %d records", prefix, len(entries))
				}
				entry := entries[0]
				if entry.Level != "ERROR" || !entry.Time.Equal(at) || entry.Fields["session_id"] != "source" || entry.Fields["request_id"] != "naming-request" || entry.Fields["provider"] != "provider" || entry.Fields["model"] != "model" {
					t.Fatalf("lost failure facts: %+v", entry)
				}
				detail := entry.Fields["detail"]
				if prefix == "audit" && (!strings.Contains(detail, "gateway detail") || strings.Contains(detail, "secret-credential") || len(detail) > 8192) {
					t.Fatalf("unsafe or missing diagnostic: %q", detail)
				}
				if prefix == "elbot" && detail != "" {
					t.Fatal("runtime exposed non-debug detail")
				}
			}
		})
	}
}

func TestNamingOwnsSynchronousLogProjection(t *testing.T) {
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Connect(func(_ context.Context, r events.LogRecord) error { records = append(records, r); return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	service := NewService(nil)
	at := time.Unix(123, 0)
	ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "wrong", RequestID: "request"})
	service.notifyNamingCompleted(ctx, NamingCompletedEvent{SessionID: "source", TriggeredAt: at, Title: "title"})
	if len(records) != 1 || records[0].Name != "session_naming_completed" || !records[0].At.Equal(at) {
		t.Fatal(records)
	}
	fields := map[string]string{}
	for _, attr := range records[0].Fields {
		fields[attr.Key] = attr.Value.String()
	}
	if fields["session_id"] != "source" || fields["request_id"] != "request" {
		t.Fatal(fields)
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.notifyNamingCompleted(ctx, NamingCompletedEvent{SessionID: "source", TriggeredAt: at})
	if len(records) != 1 {
		t.Fatal("naming log connection survived close")
	}
}

func TestNamingFallbackFailurePreservesOriginalDiagnostic(t *testing.T) {
	store := newTestStore(t)
	service := NewService(store)
	defer service.Close(context.Background())
	var failed NamingFailedEvent
	connection, err := service.NamingSignals().Failed.Connect(func(_ context.Context, event NamingFailedEvent) error { failed = event; return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	// A missing row makes the fallback save fail after the upstream failure.
	original := fmt.Errorf("upstream: %w", namingDiagnosticError{})
	service.handleNamingFailure(context.Background(), &storage.Session{ID: "missing"}, []storage.Message{{Role: storage.RoleUser, Content: "fallback text"}}, "generate title", original, "llm_error", TitleResult{}, "")
	if failed.Err != original || failed.FallbackErr == nil || failed.Reason != "generate title" || failed.Stage != "llm_error" {
		t.Fatalf("lost original failure: %+v", failed)
	}
	var diagnostic events.DiagnosticError
	if !errors.As(failed.Err, &diagnostic) {
		t.Fatal("original diagnostic no longer reachable")
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
			notifier := &fakeNamingNotifier{failures: make(chan NamingFailedEvent, 2)}
			svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, gen)
			notifier.connect(t, svc)
			appCtx, stopApp := context.WithCancel(context.Background())
			defer stopApp()
			svc.StartNaming(appCtx)
			t.Cleanup(func() { unblock.Do(func() { close(release) }); _ = svc.Close(context.Background()) })
			row := namingRow(t, svc, store)
			turnCtx, stopTurn := context.WithCancel(context.Background())
			svc.MaybeScheduleNaming(turnCtx, row.ID)
			var workerCtx context.Context
			select {
			case workerCtx = <-started:
			case <-time.After(time.Second):
				t.Fatal("no worker")
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
				t.Fatalf("cancellation failure notification: %#v", e)
			default:
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
