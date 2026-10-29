package binding

import (
	"bytes"
	"context"
	"encoding/json"
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
	for _, failure := range []string{"queue_full", "timeout"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := captureBindingLogs(t)
				m := newTestManager(t)
				if err := m.changed.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				m.changed = signal.New[BindingChangedEvent, signal.Empty]("session.binding_changed", signal.Options[BindingChangedEvent]{Capacity: 1, Concurrency: 1, Timeout: time.Second})
				scope := share.Scope{ActorID: "u"}
				old := setCurrent(t, m, scope, "old")
				finish := make(chan struct{})
				release := sync.OnceFunc(func() { close(finish) })
				defer release()
				var calls atomic.Int32
				_, err := m.ObserveBindingChanged(func(_ context.Context, event BindingChangedEvent) error {
					if event.Version != 0 {
						calls.Add(1)
					}
					<-finish
					return nil
				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if failure == "queue_full" {
					// 占满一个执行位和一个等待位，后续解绑事件无法入队。
					for range 2 {
						if _, err := m.changed.EmitAsync(context.Background(), BindingChangedEvent{}); err != nil {
							t.Fatal(err)
						}
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
				wantCalls, wantError := int32(0), signal.ErrQueueFull.Error()
				if failure == "timeout" {
					wantCalls, wantError = 1, context.DeadlineExceeded.Error()
				}
				if calls.Load() != wantCalls {
					t.Fatalf("callback calls = %d, want %d; failed event must not be retried", calls.Load(), wantCalls)
				}
				var record struct {
					Level        string `json:"level"`
					Version      uint64 `json:"version"`
					OldSessionID string `json:"old_session_id"`
					NewSessionID string `json:"new_session_id"`
					Error        string `json:"error"`
				}
				if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
					t.Fatalf("expected one failure summary, got %q: %v", logs.String(), err)
				}
				if record.Level != "WARN" || record.Version != old.Version()+1 || record.OldSessionID != "old" || record.NewSessionID != "" || record.Error != wantError {
					t.Fatalf("wrong failure summary: %+v", record)
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
			scope := share.Scope{ActorID: "u"}
			var observed BindingChangedEvent
			_, err := m.ObserveBindingChanged(func(ctx context.Context, event BindingChangedEvent) error {
				observed = event
				return ctx.Err()
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
			if observed.New != m.CurrentBinding(scope) || observed.New == nil || !observed.New.Valid() {
				t.Fatal("committed event was not delivered")
			}
			if logs.Len() != 0 {
				t.Fatalf("successful event logged a failure: %s", logs.String())
			}
		})
	}
}
