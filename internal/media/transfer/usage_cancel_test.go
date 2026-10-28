package transfer

import (
	"context"
	"errors"
	"testing"
	"time"
)

type cancelHeld struct {
	fakeHeld
	release func(context.Context) error
}

func (h *cancelHeld) Close(ctx context.Context) error { return h.release(ctx) }

func TestUsageCancellationKeepsUnstartedResources(t *testing.T) {
	m := &Manager{usages: make(map[string]*usage)}
	ctx, cancel := context.WithCancel(t.Context())
	firstCalls, secondCalls := 0, 0
	first := &cancelHeld{release: func(ctx context.Context) error { firstCalls++; cancel(); return ctx.Err() }}
	second := &cancelHeld{release: func(context.Context) error { secondCalls++; return nil }}
	id := m.registerUsage(first, "")
	m.addHeld(id, second)
	m.FinishUsages([]string{id})
	if err := m.CleanupUsages(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if firstCalls != 1 || secondCalls != 0 || len(m.usages[id].held) != 2 {
		t.Fatal("cancellation lost or released pending resources")
	}
	first.release = func(context.Context) error { firstCalls++; return nil }
	if err := m.CleanupUsages(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if len(m.usages) != 0 || firstCalls != 2 || secondCalls != 1 {
		t.Fatal("maintenance did not finish remaining resources")
	}
}

func TestUsageCleanupWaitCanCancel(t *testing.T) {
	m := &Manager{usages: make(map[string]*usage)}
	id := m.registerUsage(nil, "")
	m.FinishUsages([]string{id})
	u := m.usages[id]
	u.mu <- struct{}{}
	defer func() { <-u.mu }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.CleanupUsages(ctx, nil) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup ignored cancellation while waiting")
	}
}
