package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type lifecycleTitleGenerator struct {
	started  chan struct{}
	release  chan struct{}
	stubborn bool
}

func (g lifecycleTitleGenerator) GenerateTitle(ctx context.Context, _ []storage.Message) (session.TitleResult, error) {
	close(g.started)
	<-ctx.Done()
	if g.stubborn {
		<-g.release
	}
	return session.TitleResult{}, ctx.Err()
}

func TestRunnerOwnsNamingExitAndPartialStartup(t *testing.T) {
	for _, mode := range []string{"normal", "timeout", "partial startup"} {
		t.Run(mode, func(t *testing.T) {
			store, err := sqlite.New(context.Background(), ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			gen := lifecycleTitleGenerator{started: make(chan struct{}), release: make(chan struct{}), stubborn: mode == "timeout"}
			svc := session.NewServiceWithNaming(store, session.NamingConfig{TriggerStep: 1}, gen)
			var unblock sync.Once
			defer func() { unblock.Do(func() { close(gen.release) }); _ = svc.Close(context.Background()) }()
			row, err := svc.Create(context.Background(), session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, session.CreateRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Messages().Append(context.Background(), &storage.Message{SessionID: row.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
				t.Fatal(err)
			}
			var events []string
			runner := newTestRunner(t, &events, RunModeFull, "")
			runner.shutdownTimeout = 30 * time.Millisecond
			foundationClosed := false
			base := runner.deps.Foundation
			runner.deps.Foundation = foundationFactoryFunc(func(ctx context.Context, req FoundationRequest) (*FoundationComponents, error) {
				f, err := base.Build(ctx, req)
				f.Lifecycle = lifecycleFunc(func(context.Context) error {
					select {
					case <-svc.Done():
					default:
						t.Error("foundation released while naming was active")
					}
					foundationClosed = true
					return nil
				})
				return f, err
			})
			failure := errors.New("partial runtime failure")
			runner.deps.Runtime = runtimeFactoryFunc(func(ctx context.Context, _ RuntimeRequest) (*RuntimeComponents, error) {
				ctx, cancel := context.WithCancel(ctx)
				svc.StartNaming(ctx)
				runtime := &RuntimeComponents{Handler: handlerStub{}, Lifecycle: &runtimeLifecycle{cancel: cancel, sessions: svc}}
				if mode == "partial startup" {
					svc.MaybeScheduleNaming(ctx, row.ID)
					<-gen.started
					return runtime, failure
				}
				return runtime, nil
			})
			runner.deps.Executor = executorFunc(func(ctx context.Context, req PlatformRunRequest) error {
				svc.MaybeScheduleNaming(ctx, row.ID)
				<-gen.started
				req.Stop()
				return nil
			})
			err = runner.Run(context.Background(), Options{})
			if mode == "partial startup" {
				if !errors.Is(err, failure) {
					t.Fatalf("Run=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "timeout" {
				if foundationClosed {
					t.Fatal("closed live worker dependencies")
				}
				unblock.Do(func() { close(gen.release) })
				select {
				case <-svc.Done():
				case <-time.After(time.Second):
					t.Fatal("worker did not exit")
				}
				if foundationClosed {
					t.Fatal("unexpected detached cleanup")
				}
			} else if !foundationClosed {
				t.Fatal("foundation never closed")
			}
		})
	}
}
