package testutil

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/storage"
)

func TestNewSQLiteStoreUsesIndependentMemoryDatabase(t *testing.T) {
	ctx := context.Background()
	first := NewSQLiteStore(t)
	second := NewSQLiteStore(t)
	session := &storage.Session{ID: "same-session", OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "first"}
	if err := first.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := second.Sessions().Get(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second store Get error = %v, want not found", err)
	}
	other := *session
	other.Title = "second"
	if err := second.Sessions().Create(ctx, &other); err != nil {
		t.Fatalf("create same ID in second store: %v", err)
	}
	if got, err := first.Sessions().Get(ctx, session.ID); err != nil || got.Title != "first" {
		t.Fatalf("first store session = %+v, error = %v", got, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := second.Sessions().Get(ctx, session.ID); err != nil || got.Title != "second" {
		t.Fatalf("second store session after closing first = %+v, error = %v", got, err)
	}
	err := second.Messages().Append(ctx, &storage.Message{SessionID: "missing-session", Role: storage.RoleUser, Content: "orphan"})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("missing session Append error = %v, want foreign key violation", err)
	}
}
