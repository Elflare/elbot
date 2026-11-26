package storage

import (
	"context"
	"path/filepath"
	"testing"

	"elbot/internal/contextinfo"
)

func TestNewCanRunTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elbot_sessions.db")
	first, err := NewSupervisor(context.Background(), path, ":memory:")
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	session := &Session{ID: "fresh", Owner: contextinfo.ActorIdentity{Platform: "qq", ID: "u1"}, Platform: "qq", ChatType: "group", ChatID: "1", Title: "keep title", Metadata: `{"keep":true}`}
	if err := first.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := NewSupervisor(context.Background(), path, ":memory:")
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer second.Close()
	got, err := second.GetSession(t.Context(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != session.Owner || got.Platform != session.Platform || got.Title != session.Title || got.Metadata != session.Metadata {
		t.Fatalf("reopening changed stored session or replayed historical conversions: %+v", got)
	}
}
