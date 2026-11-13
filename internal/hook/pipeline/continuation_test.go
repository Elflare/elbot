// 验证续接与普通规则的顺序、短路，以及错误钩子的单次触发。

package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
)

func TestContinuationRunsBeforeRulesAndCanStopPropagation(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "stop"}[stop], func(t *testing.T) {
			var order, observed []string
			finished := 0
			manager := NewManager(func(context.Context, events.LogRecord) {}, func(ctx context.Context, event share.Event, observe func(context.Context, string) (context.Context, func(), error)) (share.Event, bool, error) {
				order = append(order, "route")
				_, done, _ := observe(ctx, "continuation")
				defer done()
				event.Message.Segments = llm.TextSegments("resumed")
				event.Control.StopPropagation, event.Control.Consume = stop, true
				return event, true, nil
			})
			manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Name: "ordinary", Match: share.Always(), Handler: adaptHandler(func(_ context.Context, event share.Event, _ bool) (share.Event, error) {
				order = append(order, "rules")
				if llm.SegmentsTextOnly(event.Message.Segments) != "resumed" {
					t.Fatal("ordinary rules lost continuation changes")
				}
				return event, nil
			})})
			got, err := manager.Run(t.Context(), share.Event{Point: share.PointPlatformMessageReceived}, CallOptions{Woken: true, Observe: func(ctx context.Context, name string) (context.Context, func(), error) {
				observed = append(observed, name)
				return ctx, func() { finished++ }, nil
			}})
			wantOrder, wantObserved := []string{"route", "rules"}, []string{"continuation", "ordinary"}
			if stop {
				wantOrder, wantObserved = []string{"route"}, []string{"continuation"}
			}
			if err != nil || !got.Control.Consume || !reflect.DeepEqual(order, wantOrder) || !reflect.DeepEqual(observed, wantObserved) || finished != len(wantObserved) {
				t.Fatal("order= observed= finished= event= result= err=", order, observed, finished, got, err)
			}
		})
	}
}

func TestPipelineFailureNotifiesOnceAndPreservesOriginalFailure(t *testing.T) {
	for _, source := range []string{"route", "run", "notify", "error-hook", "canceled"} {
		t.Run(source, func(t *testing.T) {
			original, secondary := errors.New("source failed"), errors.New("error hook failed")
			notified, ordinary := 0, 0
			manager := NewManager(func(context.Context, events.LogRecord) {}, func(_ context.Context, event share.Event, _ func(context.Context, string) (context.Context, func(), error)) (share.Event, bool, error) {
				if source != "route" {
					return event, false, nil
				}
				event.Message.Segments = llm.TextSegments("failed route edit")
				return event, true, original
			})
			manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Name: "failure", Match: share.Always(), Handler: adaptHandler(func(_ context.Context, event share.Event, _ bool) (share.Event, error) {
				ordinary++
				event.Message.Segments = llm.TextSegments("failed rule edit")
				return event, original
			})})
			manager.Register(share.Registration{Point: share.PointErrorOccurred, Name: "error", Match: share.Always(), Handler: adaptHandler(func(_ context.Context, event share.Event, _ bool) (share.Event, error) {
				notified++
				if !errors.Is(event.Error, original) {
					t.Errorf("lost source error: %v", event.Error)
				}
				return event, secondary
			})})
			event := share.Event{Point: share.PointPlatformMessageReceived, Message: share.MessagePayload{Segments: llm.TextSegments("original")}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if source == "canceled" {
				cancel()
			}
			var err error
			var wantError error
			wantNotified := 1
			switch source {
			case "error-hook":
				event.Point, event.Error = share.PointErrorOccurred, original
				err = manager.Notify(ctx, event, CallOptions{Woken: true})
			case "notify":
				err = manager.Notify(ctx, event, CallOptions{Woken: true})
			default:
				var got share.Event
				got, err = manager.Run(ctx, event, CallOptions{Woken: true})
				if llm.SegmentsTextOnly(got.Message.Segments) != "original" {
					t.Fatal("failed changes escaped")
				}
			}
			if source == "canceled" {
				wantError = original
				wantNotified = 0
			}
			if source == "route" {
				wantNotified = 2
			}
			if !errors.Is(err, wantError) || notified != wantNotified {
				t.Fatalf("notified=%d err=%v", notified, err)
			}
			if source != "error-hook" && (errors.Is(err, secondary)) {
				t.Fatalf("source failure replaced: %v", err)
			}
			if source == "route" && ordinary != 1 {
				t.Fatalf("ordinary=%d", ordinary)
			}
		})
	}
}
