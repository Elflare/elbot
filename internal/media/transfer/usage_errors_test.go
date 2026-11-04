package transfer

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/media/share"
	"errors"
	"strings"
	"testing"
)

func TestCleanupRecordsIndependentFailuresBeforeCancellation(t *testing.T) {
	var records []events.LogRecord
	m := NewManager(nil, Options{Log: func(_ context.Context, r events.LogRecord) { records = append(records, r) }})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := &cancelHeld{fakeHeld: fakeHeld{info: share.MediaInfo{ID: "first"}}, release: func(context.Context) error { return errors.New("first failure") }}
	second := &cancelHeld{fakeHeld: fakeHeld{info: share.MediaInfo{ID: "second"}}, release: func(context.Context) error { cancel(); return errors.New("second failure") }}
	id := m.registerUsage(first, "")
	m.addHeld(id, second)
	m.FinishUsages([]string{id})
	if err := m.CleanupUsages(ctx, nil); err != context.Canceled {
		t.Fatalf("interruption: %v", err)
	}
	if len(records) != 2 || !strings.Contains(records[0].Content, "first failure") || !strings.Contains(records[1].Content, "second failure") {
		t.Fatalf("independent failures: %+v", records)
	}
	for _, r := range records {
		if !strings.Contains(r.Content, id) || !strings.Contains(r.Content, "operation=release_hold") {
			t.Fatalf("missing context: %+v", r)
		}
	}
	if len(m.usages[id].held) != 2 {
		t.Fatal("failed resources lost")
	}
	first.release = func(context.Context) error { return nil }
	second.release = first.release
	if err := m.CleanupUsages(t.Context(), nil); err != nil || len(m.usages) != 0 {
		t.Fatalf("retry: %v", err)
	}
}
