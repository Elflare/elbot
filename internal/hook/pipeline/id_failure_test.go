package pipeline

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/events"
	"elbot/internal/hook/share"
)

func TestRequestAllocationFailureSkipsHandler(t *testing.T) {
	for _, want := range []error{errors.New("reserve failed"), context.Canceled, context.DeadlineExceeded} {
		m := NewManager(nil, nil)
		_, _, result, err := m.invoke(t.Context(), registration{handler: share.HandlerFunc(func(context.Context, share.Event, bool) (share.Event, events.LogResult, error) {
			t.Fatal("handler ran after allocation failure")
			return share.Event{}, events.LogResult{}, nil
		})}, share.Event{}, CallOptions{Observe: func(ctx context.Context, _ string) (context.Context, func(), error) {
			return ctx, nil, want
		}})
		wantStatus := events.ResultFailed
		if errors.Is(want, context.Canceled) || errors.Is(want, context.DeadlineExceeded) {
			wantStatus = events.ResultCanceled
		}
		if !errors.Is(err, want) || result.ResultStatus != wantStatus || len(m.active) != 0 {
			t.Fatalf("err=%v result=%+v active=%v", err, result, m.active)
		}
	}
}
