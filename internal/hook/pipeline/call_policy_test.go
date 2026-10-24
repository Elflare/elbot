package pipeline

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"testing"
)

func TestReadOnlyCallsReachesRunAndNotifyHandlers(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		for _, notify := range []bool{false, true} {
			manager := newTestManager()
			calls := 0
			manager.Register(share.Registration{Point: share.PointToolCallPrepared, Name: "policy", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, got bool) (share.Event, events.LogResult, error) {
				calls++
				if got != readOnly {
					t.Errorf("read-only policy=%v, want %v", got, readOnly)
				}
				return event, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
			})})
			event := share.Event{Point: share.PointToolCallPrepared}
			options := CallOptions{Woken: true, ReadOnlyCalls: readOnly}
			var err error
			if notify {
				_, err = manager.Notify(t.Context(), event, options)
			} else {
				_, _, err = manager.Run(t.Context(), event, options)
			}
			if err != nil || calls != 1 {
				t.Fatalf("notify=%v calls=%d err=%v", notify, calls, err)
			}
		}
	}
}
