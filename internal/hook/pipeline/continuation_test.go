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
			manager := NewManager(func(context.Context, events.LogRecord) {}, func(ctx context.Context, event share.Event, observe func(context.Context, string) (context.Context, func())) (share.Event, bool, events.LogResult, error) {
				order = append(order, "route")
				_, done := observe(ctx, "continuation")
				defer done()
				event.Message.Segments = llm.TextSegments("resumed")
				event.Control.StopPropagation, event.Control.Consume = stop, true
				return event, true, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "continuation"}, nil
			})
			manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Name: "ordinary", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event) (share.Event, events.LogResult, error) {
				order = append(order, "rules")
				if llm.SegmentsTextOnly(event.Message.Segments) != "resumed" {
					t.Fatal("ordinary rules lost continuation changes")
				}
				return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "ordinary"}, nil
			})})
			got, result, err := manager.Run(t.Context(), share.Event{Point: share.PointPlatformMessageReceived}, CallOptions{Woken: true, Observe: func(ctx context.Context, name string) (context.Context, func()) {
				observed = append(observed, name)
				return ctx, func() { finished++ }
			}})
			wantOrder, wantObserved, wantResult := []string{"route", "rules"}, []string{"continuation", "ordinary"}, "ordinary"
			if stop {
				wantOrder, wantObserved, wantResult = []string{"route"}, []string{"continuation"}, "continuation"
			}
			if err != nil || !got.Control.Consume || !reflect.DeepEqual(order, wantOrder) || !reflect.DeepEqual(observed, wantObserved) || finished != len(wantObserved) || result.Content != wantResult {
				t.Fatalf("order=%v observed=%v finished=%d event=%+v result=%+v err=%v", order, observed, finished, got, result, err)
			}
		})
	}
}

func TestPipelineFailureNotifiesOnceAndPreservesOriginalFailure(t *testing.T) {
	for _, source := range []string{"route", "run", "notify", "error-hook", "canceled"} {
		t.Run(source, func(t *testing.T) {
			original, secondary := errors.New("source failed"), errors.New("error hook failed")
			notified, ordinary := 0, 0
			manager := NewManager(func(context.Context, events.LogRecord) {}, func(_ context.Context, event share.Event, _ func(context.Context, string) (context.Context, func())) (share.Event, bool, events.LogResult, error) {
				if source != "route" {
					return event, false, events.LogResult{ResultStatus: events.ResultSkipped}, nil
				}
				event.Message.Segments = llm.TextSegments("failed route edit")
				return event, true, events.LogResult{ResultStatus: events.ResultFailed, Content: "source outcome"}, original
			})
			manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Name: "failure", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event) (share.Event, events.LogResult, error) {
				ordinary++
				event.Message.Segments = llm.TextSegments("failed rule edit")
				return event, events.LogResult{ResultStatus: events.ResultFailed, Content: "source outcome"}, original
			})})
			manager.Register(share.Registration{Point: share.PointErrorOccurred, Name: "error", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event) (share.Event, events.LogResult, error) {
				notified++
				if !errors.Is(event.Error, original) {
					t.Errorf("lost source error: %v", event.Error)
				}
				return event, events.LogResult{ResultStatus: events.ResultFailed}, secondary
			})})
			event := share.Event{Point: share.PointPlatformMessageReceived, Message: share.MessagePayload{Segments: llm.TextSegments("original")}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if source == "canceled" {
				cancel()
			}
			var result events.LogResult
			var err error
			wantError, wantNotified := original, 1
			switch source {
			case "error-hook":
				event.Point, event.Error = share.PointErrorOccurred, original
				result, err = manager.Notify(ctx, event, CallOptions{Woken: true})
				wantError = secondary
			case "notify":
				result, err = manager.Notify(ctx, event, CallOptions{Woken: true})
			default:
				var got share.Event
				got, result, err = manager.Run(ctx, event, CallOptions{Woken: true})
				if llm.SegmentsTextOnly(got.Message.Segments) != "original" {
					t.Fatal("failed changes escaped")
				}
			}
			if source == "canceled" {
				wantNotified = 0
			}
			if !errors.Is(err, wantError) || notified != wantNotified {
				t.Fatalf("notified=%d err=%v", notified, err)
			}
			if source != "error-hook" && (errors.Is(err, secondary) || result.ResultStatus != events.ResultFailed) {
				t.Fatalf("source failure replaced: %+v %v", result, err)
			}
			if source == "route" && (ordinary != 0 || result.Content != "source outcome") {
				t.Fatalf("ordinary=%d result=%+v", ordinary, result)
			}
		})
	}
}
