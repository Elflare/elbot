package pipeline

import (
	"context"
	"elbot/internal/hook/share"
	"errors"
	"testing"
)

func TestInvocationReleasesContext(t *testing.T) {
	for _, notify := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			manager := newTestManager()
			var captured context.Context
			done := 0
			failure := errors.New("handler failed")
			manager.Register(share.Registration{Point: share.PointToolCallPrepared, Name: "cleanup", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, error) {
				captured = ctx
				if manager.List()[0].Active != 1 {
					t.Error("handler not active")
				}
				if fail {
					return event, failure
				}
				return event, nil
			})})
			options := CallOptions{Woken: true, Observe: func(ctx context.Context, _ string) (context.Context, func(), error) {
				return ctx, func() { done++ }, nil
			}}
			event := share.Event{Point: share.PointToolCallPrepared}
			var err error
			if notify {
				err = manager.Notify(t.Context(), event, options)
			} else {
				_, err = manager.Run(t.Context(), event, options)
			}
			if errors.Is(err, failure) != fail || done != 1 || captured.Err() != context.Canceled || t.Context().Err() != nil || manager.List()[0].Active != 0 {
				t.Fatalf("notify=%v fail=%v err=%v done=%d context=%v", notify, fail, err, done, captured.Err())
			}
		}
	}
}

func TestInvokeCleanupOnPanic(t *testing.T) {
	for _, stage := range []string{"observe", "handler", "done"} {
		manager := newTestManager()
		var captured context.Context
		reg := registration{name: "panic", handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, error) {
			if stage == "handler" {
				panic(stage)
			}
			return event, nil
		})}
		func() {
			defer func() {
				if recover() != stage {
					t.Error("panic not propagated")
				}
			}()
			manager.invoke(t.Context(), reg, share.Event{}, CallOptions{Observe: func(ctx context.Context, _ string) (context.Context, func(), error) {
				captured = ctx
				if stage == "observe" {
					panic(stage)
				}
				return ctx, func() {
					if stage == "done" {
						panic(stage)
					}
				}, nil
			}})
		}()
		if captured.Err() != context.Canceled || len(manager.active) != 0 {
			t.Fatalf("%s did not clean up", stage)
		}
	}
}

func TestReadOnlyCallsReachesRunAndNotifyHandlers(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		for _, notify := range []bool{false, true} {
			manager := newTestManager()
			calls := 0
			manager.Register(share.Registration{Point: share.PointToolCallPrepared, Name: "policy", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, got bool) (share.Event, error) {
				calls++
				if got != readOnly {
					t.Errorf("read-only policy=%v, want %v", got, readOnly)
				}
				return event, nil
			})})
			event := share.Event{Point: share.PointToolCallPrepared}
			options := CallOptions{Woken: true, ReadOnlyCalls: readOnly}
			var err error
			if notify {
				err = manager.Notify(t.Context(), event, options)
			} else {
				_, err = manager.Run(t.Context(), event, options)
			}
			if err != nil || calls != 1 {
				t.Fatalf("notify=%v calls=%d err=%v", notify, calls, err)
			}
		}
	}
}
