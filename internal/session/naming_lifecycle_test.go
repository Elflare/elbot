package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/storage"
)

type namingGeneratorFunc func(context.Context, []storage.Message) (TitleResult, error)

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
			svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, gen, notifier)
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
	}), nil)
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
	}), nil)
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
