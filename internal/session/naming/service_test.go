package naming

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func TestForgetDuringPreparationGenerationAndCompletion(t *testing.T) {
	for _, phase := range []string{"preparation", "generation", "completion"} {
		t.Run(phase, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var calls atomic.Int32
			f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
				calls.Add(1)
				if phase == "generation" {
					close(started)
					<-release
				}
				return llm.TextResult{Text: "generated"}, nil
			})
			t.Cleanup(unblock)
			id := f.row(t, storage.Message{Role: storage.RoleUser, Content: "user"})
			if phase == "preparation" {
				f.s.deps.Messages = messageFunc(func(ctx context.Context, id string) ([]storage.Message, error) {
					close(started)
					<-release
					return f.store.Messages().ListBySession(ctx, id)
				})
			}
			if phase == "completion" {
				c, err := f.s.namingSignals.Completed.Observe(func(context.Context, NamingCompletedEvent) error { close(started); <-release; return nil }, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Disconnect()
			}
			triggerDone := make(chan struct{})
			go func() { f.s.MaybeScheduleNaming(context.Background(), id); close(triggerDone) }()
			receive(t, started)
			f.s.ForgetSession(id)
			f.s.ForgetSession(id)
			f.s.mu.Lock()
			state := f.s.namingStates[id]
			retained := state != nil && state.inFlight && state.retired
			f.s.mu.Unlock()
			if !retained {
				t.Fatal("retired task lost reservation")
			}
			f.s.MaybeScheduleNaming(t.Context(), id)
			unblock()
			receive(t, triggerDone)
			receive(t, f.completed)
			f.idle(t, id)
			f.s.mu.Lock()
			_, exists := f.s.namingStates[id]
			f.s.mu.Unlock()
			if exists || calls.Load() != 1 {
				t.Fatalf("late completion recreated state: exists=%v calls=%d", exists, calls.Load())
			}
			f.s.MaybeScheduleNaming(t.Context(), id)
			if calls.Load() != 1 {
				t.Fatal("retired successful task did not persist completion")
			}
		})
	}
}

func TestCloseWaitsAfterEmissionTimedOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, err := NewService(Config{}, ServiceDeps{})
		if err != nil {
			t.Fatal(err)
		}
		started, release := make(chan struct{}), make(chan struct{})
		c, err := s.namingSignals.Completed.Observe(func(context.Context, NamingCompletedEvent) error { close(started); <-release; return nil }, signal.ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(context.Background())
		defer close(release)
		id := "session"
		emitted := make(chan struct{})
		go func() {
			s.notifyNamingCompleted(context.Background(), NamingCompletedEvent{Identity: events.LogIdentity{SessionID: &id}, ResultStatus: events.ResultSucceeded})
			close(emitted)
		}()
		<-started
		time.Sleep(31 * time.Second)
		synctest.Wait()
		select {
		case <-emitted:
		default:
			t.Fatal("emission did not report timeout")
		}
		budget, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(budget); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close=%v", err)
		}
		select {
		case <-s.Done():
			t.Fatal("Done ignored actual callback")
		default:
		}
		// 关闭等待由同一流程续接，调用方超时不能提前清掉所有权。
		go func() { <-c.Done(); _ = s.Close(context.Background()) }()
	})
}

func TestPreparationReservationReleasedBelowThresholdAndAfterCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "below threshold", true: "cancelled"}[cancelled], func(t *testing.T) {
			f := newFixture(t, 2, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
				return llm.TextResult{Text: "generated"}, nil
			})
			id := f.row(t, storage.Message{Role: storage.RoleUser, Content: "user"})
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			f.s.deps.Messages = messageFunc(func(ctx context.Context, id string) ([]storage.Message, error) {
				close(started)
				<-release
				return f.store.Messages().ListBySession(ctx, id)
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			go func() { f.s.MaybeScheduleNaming(ctx, id); close(done) }()
			receive(t, started)
			f.s.MaybeScheduleNaming(t.Context(), id)
			if cancelled {
				cancel()
			}
			close(release)
			receive(t, done)
			if cancelled {
				e := receive(t, f.completed)
				if e.PreparationResultStatus != events.ResultCanceled || e.FailureCount != 0 {
					t.Fatalf("event=%+v", e)
				}
			}
			f.s.deps.Messages = f.store.Messages()
			if err := f.store.Messages().Append(t.Context(), &storage.Message{SessionID: id, Role: storage.RoleAssistant, Content: "answer"}); err != nil {
				t.Fatal(err)
			}
			if e := f.attempt(t, id); e.ResultStatus != events.ResultSucceeded {
				t.Fatalf("event=%+v", e)
			}
		})
	}
}

func TestTurnCancellationAndCloseWaitForIgnoringClient(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "late success", true: "late failure"}[failure], func(t *testing.T) {
			started, release := make(chan context.Context, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			f := newFixture(t, 1, func(ctx context.Context, _ llm.TextRequest) (llm.TextResult, error) {
				started <- ctx
				<-release
				if failure {
					return llm.TextResult{}, errors.New("late failure")
				}
				return llm.TextResult{Text: "late title"}, nil
			})
			t.Cleanup(unblock)
			id := f.row(t, storage.Message{Role: storage.RoleUser, Content: "user"})
			turn, cancel := context.WithCancel(t.Context())
			defer cancel()
			type privateKey struct{}
			turn = context.WithValue(turn, privateKey{}, "turn-only reference")
			model := contextinfo.Model{Provider: "current", Model: "dialogue", APIType: "chat"}
			turn = contextinfo.WithModel(turn, model)
			turn = contextinfo.WithExecution(turn, contextinfo.Execution{RequestID: "request", RunID: "run"})
			f.s.MaybeScheduleNaming(turn, id)
			worker := receive(t, started)
			cancel()
			if worker.Err() != nil || worker.Value(privateKey{}) != nil {
				t.Fatal("worker retained Turn lifetime or private values")
			}
			if got, ok := contextinfo.ModelFromContext(worker); !ok || got != model {
				t.Fatalf("model=%+v", got)
			}
			facts, _ := contextinfo.ExecutionFromContext(worker)
			if facts.SessionID != id || facts.RequestID != "request" {
				t.Fatalf("facts=%+v", facts)
			}
			f.s.BeginClose()
			receive(t, worker.Done())
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer stop()
			if err := f.s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Close=%v", err)
			}
			select {
			case <-f.s.Done():
				t.Fatal("Done closed before client returned")
			default:
			}
			unblock()
			if err := f.s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			e := receive(t, f.completed)
			if e.ResultStatus != events.ResultCanceled || e.FailureCount != 0 || e.PersistenceResultStatus != events.ResultUnspecified || e.FallbackResultStatus != events.ResultUnspecified {
				t.Fatalf("event=%+v", e)
			}
			row, _ := f.store.Sessions().Get(t.Context(), id)
			if row.Title != "original" {
				t.Fatalf("late write: %+v", row)
			}
			if err := f.s.Start(t.Context()); !errors.Is(err, signal.ErrClosed) {
				t.Fatalf("restart=%v", err)
			}
		})
	}
}

func TestCloseWaitsForPreparationAndRealTerminalCallback(t *testing.T) {
	for _, phase := range []string{"preparation", "completion"} {
		t.Run(phase, func(t *testing.T) {
			f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
				return llm.TextResult{Text: "title"}, nil
			})
			id := f.row(t, storage.Message{Role: storage.RoleUser, Content: "user"})
			started, release, triggerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			if phase == "preparation" {
				f.s.deps.Messages = messageFunc(func(ctx context.Context, id string) ([]storage.Message, error) {
					close(started)
					<-release
					return f.store.Messages().ListBySession(ctx, id)
				})
			} else {
				c, err := f.s.namingSignals.Completed.Observe(func(context.Context, NamingCompletedEvent) error { close(started); <-release; return nil }, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Disconnect()
			}
			go func() { f.s.MaybeScheduleNaming(context.Background(), id); close(triggerDone) }()
			receive(t, started)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()
			if err := f.s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Close=%v", err)
			}
			select {
			case <-f.s.Done():
				t.Fatal("premature Done")
			default:
			}
			unblock()
			receive(t, triggerDone)
			if err := f.s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNeverStartedAndConcurrentStartClose(t *testing.T) {
	for i := 0; i < 20; i++ {
		s, err := NewService(Config{TriggerStep: 1}, ServiceDeps{})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for n := 0; n < 8; n++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				if n%2 == 0 && i != 0 {
					_ = s.Start(context.Background())
				} else {
					if err := s.Close(context.Background()); err != nil {
						t.Error(err)
					}
				}
			}(n)
		}
		wg.Wait()
		if err := s.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-s.Done():
		default:
			t.Fatal("Done still open")
		}
	}
}

func TestParentCancellationStopsAdmission(t *testing.T) {
	s, err := NewService(Config{TriggerStep: 1}, ServiceDeps{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	s.MaybeScheduleNaming(t.Context(), "no dependencies may be used")
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
