package testutil

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"errors"
	"strings"
	"testing"

	"elbot/internal/storage"
)

func TestNewSQLiteStoreUsesIndependentMemoryDatabase(t *testing.T) {
	ctx := context.Background()
	first := NewSQLiteStore(t)
	second := NewSQLiteStore(t)
	session := &storage.Session{ID: "same-session", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "first", ChatType: "private"}
	if err := first.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := second.GetSession(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second store Get error = %v, want not found", err)
	}
	other := *session
	other.Title = "second"
	if err := second.CreateSession(ctx, &other); err != nil {
		t.Fatalf("create same ID in second store: %v", err)
	}
	if got, err := first.GetSession(ctx, session.ID); err != nil || got.Title != "first" {
		t.Fatalf("first store session = %+v, error = %v", got, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := second.GetSession(ctx, session.ID); err != nil || got.Title != "second" {
		t.Fatalf("second store session after closing first = %+v, error = %v", got, err)
	}
	err := second.AppendMessage(ctx, &storage.Message{ID: storage.NewID(), SessionID: "missing-session", Role: llm.RoleUser, Content: "orphan"})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("missing session Append error = %v, want foreign key violation", err)
	}
}
