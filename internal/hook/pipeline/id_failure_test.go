package pipeline

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/hook/share"
)

func TestRequestAllocationFailureSkipsHandler(t *testing.T) {
	for _, want := range []error{errors.New("reserve failed"), context.Canceled, context.DeadlineExceeded} {
		m := NewManager(nil, nil)
		_, _, err := m.invoke(t.Context(), registration{handler: share.HandlerFunc(func(context.Context, share.Event, bool) (share.Event, error) {
			t.Fatal("handler ran after allocation failure")
			return share.Event{}, nil
		})}, share.Event{}, CallOptions{Observe: func(ctx context.Context, _ string) (context.Context, func(), error) {
			return ctx, nil, want
		}})
		if !errors.Is(err, want) || len(m.active) != 0 {
			t.Fatal("err= result= active=", err, m.active)
		}
	}
}
