package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/storage"
)

func TestPlatformMessageAliasesPersistAndExpireWithHistory(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := NewChatHistory(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := storage.Now().Add(-time.Hour)
	old := cutoff.Add(-time.Hour)
	mappings := []storage.PlatformMessageAlias{
		{Platform: "qqofficial", PlatformScopeID: "group:1", Alias: "REFIDX_same", PlatformMessageID: "first", CreatedAt: old},
		{Platform: "qqofficial", PlatformScopeID: "group:2", Alias: "REFIDX_same", PlatformMessageID: "second"},
		{Platform: "other", PlatformScopeID: "group:1", Alias: "REFIDX_same", PlatformMessageID: "third"},
	}
	for _, mapping := range mappings {
		for range 2 {
			if err := db.Repository().MapPlatformMessageAlias(ctx, mapping); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Repository().Append(ctx, &storage.ChatMessage{
		Platform: "qqofficial", PlatformScopeID: "group:1", PlatformMessageID: "first", SenderID: "user", Text: "old", CreatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = NewChatHistory(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := db.Repository()
	for _, mapping := range mappings {
		got, err := repo.ResolvePlatformMessageAlias(ctx, mapping.Platform, mapping.PlatformScopeID, mapping.Alias)
		if err != nil || got != mapping.PlatformMessageID {
			t.Fatalf("resolve %+v: %q, %v", mapping, got, err)
		}
	}
	if _, err := repo.ResolvePlatformMessageAlias(ctx, "qqofficial", "c2c:1", "REFIDX_same"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing alias: %v", err)
	}
	deleted, err := repo.DeleteBefore(ctx, cutoff)
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup: count=%d err=%v", deleted, err)
	}
	if _, err := repo.ResolvePlatformMessageAlias(ctx, "qqofficial", "group:1", "REFIDX_same"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired alias: %v", err)
	}
	for _, mapping := range mappings[1:] {
		if got, err := repo.ResolvePlatformMessageAlias(ctx, mapping.Platform, mapping.PlatformScopeID, mapping.Alias); err != nil || got != mapping.PlatformMessageID {
			t.Fatalf("retained alias: %q, %v", got, err)
		}
	}
}
