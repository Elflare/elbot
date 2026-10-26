package agent

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/storage"
)

func TestBackgroundTakeoverQueryUsesOwnedSessionAndCancellation(t *testing.T) {
	m, deps, _, ctx := newBackgroundAgent(t)
	row := &storage.Session{ID: "promoted", OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "cli", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Metadata: `{"foreground_origin":{"kind":"cron"}}`}
	if err := deps.Store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "missing", "promoted"} {
		taken, err := m.SessionTakenOver(ctx, id)
		if err != nil || taken != (id == "promoted") {
			t.Fatalf("%q=%v,%v", id, taken, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.SessionTakenOver(canceled, row.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation=%v", err)
	}
	m.BeginClose()
	if _, err := m.SessionTakenOver(ctx, row.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("agent cancellation=%v", err)
	}
}
