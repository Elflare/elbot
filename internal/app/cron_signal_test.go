package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	elcron "elbot/internal/cron"
	"elbot/internal/hook"
	"elbot/internal/platform"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type connectionCronRepo struct {
	storage.CronJobRepository
	seen chan struct{}
}

func (r *connectionCronRepo) ListEnabled(ctx context.Context) ([]storage.CronJob, error) {
	select {
	case r.seen <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}

type connectionCronStore struct {
	storage.Store
	repo *connectionCronRepo
}

func (s connectionCronStore) CronJobs() storage.CronJobRepository { return s.repo }

func TestCronConnectionRecoveryIndependentOfUserHooks(t *testing.T) {
	for _, mode := range []string{"stop", "error", "block"} {
		t.Run(mode, func(t *testing.T) {
			repo := &connectionCronRepo{seen: make(chan struct{}, 2)}
			cron := elcron.NewService(elcron.Options{Store: connectionCronStore{repo: repo}})
			hooks := hook.NewManager()
			started := make(chan struct{})
			var once sync.Once
			if err := hooks.Register(hook.Registration{Point: hook.PointPlatformConnected, Name: "user", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, e hook.Event) (hook.Event, error) {
				once.Do(func() { close(started) })
				switch mode {
				case "stop":
					e.Control.StopPropagation = true
				case "error":
					return e, errors.New("user failure")
				case "block":
					<-ctx.Done()
				}
				return e, nil
			})}); err != nil {
				t.Fatal(err)
			}
			agt := &signalAgent{notify: func(ctx context.Context, name string) {
				_, _ = hooks.Run(ctx, hook.Event{Point: hook.PointPlatformConnected, Platform: hook.PlatformContext{Name: name}})
			}}
			p := &signalPlatform{name: "qqonebot", connected: signal.New[platform.ConnectedEvent]("review")}
			b := &signalBindings{}
			if err := b.connectPlatforms(agt, cron, []platformRuntime{p}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := b.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			for i := 0; i < 2; i++ {
				if err := p.connected.Emit(context.Background(), platform.ConnectedEvent{Platform: p.name}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-repo.seen:
				case <-time.After(time.Second):
					t.Fatal("user hook blocked Cron recovery")
				}
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("platform hook was not delivered")
			}
		})
	}
}
