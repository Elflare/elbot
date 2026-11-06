package binding

import (
	"bytes"
	"context"
	"elbot/internal/contextinfo"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/session/share"
	"elbot/internal/signal"
)

func captureBindingLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestBindingEventFailurePreservesCommit(t *testing.T) {
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"queue_full"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := captureBindingLogs(t)
				m := newTestManager(t)
				if err := m.changed.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				m.changed = signal.New[BindingChangedEvent, signal.Empty]("session.binding_changed", signal.Options[BindingChangedEvent]{Capacity: 1, Concurrency: 1, Timeout: time.Second})
				scope := share.Scope{Owner: contextinfo.ActorIdentity{Platform: "", ID: "u"}}
				old := setCurrent(t, m, scope, "old")
				finish := make(chan struct{})
				release := sync.OnceFunc(func() { close(finish) })
				defer release()
				var calls atomic.Int32
				_, err := m.ObserveBindingChanged(func(_ context.Context, event BindingChangedEvent) {
					if event.Version != 0 {
						calls.Add(1)
					}
					<-finish

				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if failure == "queue_full" {
					// 占满一个执行位和一个等待位，后续解绑事件无法入队。
					for range 2 {
						m.changed.EmitAsync(context.Background(), BindingChangedEvent{})
						synctest.Wait()
					}
				}
				setCurrent(t, m, scope, "")
				if old.Valid() || m.CurrentBinding(scope) != nil {
					t.Fatal("failed event rolled back the committed unbinding")
				}
				release()
				synctest.Wait()
				if err := m.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 0 {
					t.Fatal("rejected event was retried")
				}
				if logs.Len() != 0 {
					t.Fatalf("framework failure reported as business error: %s", logs.String())
				}
			})
		})
	}
}

func TestBindingEventSuccessDoesNotWarn(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "canceled_request"}[canceled], func(t *testing.T) {
			logs := captureBindingLogs(t)
			m := newTestManager(t)
			scope := share.Scope{Owner: contextinfo.ActorIdentity{Platform: "", ID: "u"}}
			observations := make(chan BindingChangedEvent, 1)
			_, err := m.ObserveBindingChanged(func(ctx context.Context, event BindingChangedEvent) {
				observations <- event

			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, release, err := m.EnterActivation(ctx, scope, "new")
			if err != nil {
				t.Fatal(err)
			}
			m.SetCurrent(ctx, scope, "new")
			if canceled {
				cancel()
			}
			release()
			observed := <-observations
			if observed.New != m.CurrentBinding(scope) || observed.New == nil || !observed.New.Valid() {
				t.Fatal("committed event was not delivered")
			}
			if logs.Len() != 0 {
				t.Fatalf("successful event logged a failure: %s", logs.String())
			}
		})
	}
}
