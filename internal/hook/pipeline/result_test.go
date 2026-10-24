package pipeline

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/events"
	"elbot/internal/hook/share"
)

func TestPipelineAggregatesDeclaredOutcomes(t *testing.T) {
	for _, notify := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			statuses []events.ResultStatus
			want     events.LogResult
		}{
			{"skipped", []events.ResultStatus{events.ResultSkipped}, events.LogResult{ResultStatus: events.ResultSkipped, Content: "no matching Hook"}},
			{"failure-sticks", []events.ResultStatus{events.ResultSucceeded, events.ResultFailed, events.ResultSucceeded}, events.LogResult{ResultStatus: events.ResultFailed, Content: "a\nb\nc"}},
			{"cancel-sticks", []events.ResultStatus{events.ResultCanceled, events.ResultSucceeded}, events.LogResult{ResultStatus: events.ResultCanceled, Content: "a\nb"}},
			{"failure-beats-cancel", []events.ResultStatus{events.ResultCanceled, events.ResultFailed}, events.LogResult{ResultStatus: events.ResultFailed, Content: "a\nb"}},
			{"cancel-after-failure", []events.ResultStatus{events.ResultFailed, events.ResultCanceled}, events.LogResult{ResultStatus: events.ResultFailed, Content: "a\nb"}},
			{"skip-body", []events.ResultStatus{events.ResultSucceeded, events.ResultSkipped, events.ResultSucceeded}, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "a\nc"}},
			{"last-other-status", []events.ResultStatus{events.ResultRejected, events.ResultUnspecified}, events.LogResult{ResultStatus: events.ResultUnspecified, Content: "a\nb"}},
		} {
			t.Run(tc.name+map[bool]string{false: "/run", true: "/notify"}[notify], func(t *testing.T) {
				manager := NewManager(func(context.Context, events.LogRecord) {}, nil)
				for i, status := range tc.statuses {
					manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
						return event, events.LogResult{ResultStatus: status, Content: string(rune('a' + i))}, nil
					})})
				}
				event := share.Event{Point: share.PointPlatformMessageReceived}
				var result events.LogResult
				var err error
				if notify {
					result, err = manager.Notify(t.Context(), event, CallOptions{Woken: true})
				} else {
					_, result, err = manager.Run(t.Context(), event, CallOptions{Woken: true})
				}
				if err != nil || result != tc.want {
					t.Fatalf("result=%+v err=%v, want=%+v", result, err, tc.want)
				}
			})
		}
	}
}

func TestContinuationOutcomeSurvivesOrdinaryRules(t *testing.T) {
	for _, status := range []events.ResultStatus{events.ResultSucceeded, events.ResultFailed, events.ResultCanceled} {
		for _, ordinary := range []bool{false, true} {
			t.Run(status.String()+map[bool]string{false: "/no-match", true: "/ordinary"}[ordinary], func(t *testing.T) {
				manager := NewManager(func(context.Context, events.LogRecord) {}, func(_ context.Context, event share.Event, _ func(context.Context, string) (context.Context, func())) (share.Event, bool, events.LogResult, error) {
					return event, true, events.LogResult{ResultStatus: status, Content: "continuation"}, nil
				})
				want := "continuation"
				if ordinary {
					want += "\nordinary"
					manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
						return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "ordinary"}, nil
					})})
				}
				_, result, err := manager.Run(t.Context(), share.Event{Point: share.PointPlatformMessageReceived}, CallOptions{Woken: true})
				if err != nil || result.ResultStatus != status || result.Content != want {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			})
		}
	}
}

func TestContinuationAndOrdinaryFailureKeepBothOutcomes(t *testing.T) {
	failure := errors.New("ordinary failed")
	manager := NewManager(func(context.Context, events.LogRecord) {}, func(_ context.Context, event share.Event, _ func(context.Context, string) (context.Context, func())) (share.Event, bool, events.LogResult, error) {
		return event, true, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "continuation"}, nil
	})
	manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
		return event, events.LogResult{ResultStatus: events.ResultFailed, Content: "ordinary"}, failure
	})})
	_, result, err := manager.Run(t.Context(), share.Event{Point: share.PointPlatformMessageReceived}, CallOptions{Woken: true})
	if !errors.Is(err, failure) || result.ResultStatus != events.ResultFailed || result.Content != "continuation\nordinary" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
