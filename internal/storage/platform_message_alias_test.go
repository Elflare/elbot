package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestUpgradeAliasSendersFromScopedHistory(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := openDB(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, createChatHistorySQL+`
INSERT INTO chat_messages(id,platform,chat_type,chat_id,platform_message_id,sender_id,text,created_at) VALUES
 ('1','qqofficial','group','g','cmd','bot','status','today'),
 ('2','qqofficial','private','g','cmd','other','hi','today'),
 ('3','qqofficial','group','g2','cmd','member','hi','today');
INSERT INTO platform_message_aliases(platform,chat_type,chat_id,alias,platform_message_id,created_at) VALUES
 ('qqofficial','group','g','q','cmd','today'),
 ('qqofficial','group','g','missing','absent','today');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openHistoryForTest(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for alias, want := range map[string]string{"q": "bot", "missing": ""} {
		_, sender, err := store.ResolvePlatformMessageAlias(ctx, "qqofficial", "group", "g", alias)
		if err != nil || sender != want {
			t.Errorf("%s sender=%q want %q, err=%v", alias, sender, want, err)
		}
	}
}

func TestPlatformMessageAliasesPersistAndExpireWithHistory(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := openHistoryForTest(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := Now().Add(-time.Hour)
	old := cutoff.Add(-time.Hour)
	mappings := []PlatformMessageAlias{
		{Platform: "qqofficial", ChatType: "group", ChatID: "1", Alias: "REFIDX_same", PlatformMessageID: "first", SenderID: "one", CreatedAt: old},
		{Platform: "qqofficial", ChatType: "group", ChatID: "2", Alias: "REFIDX_same", PlatformMessageID: "second", SenderID: "two"},
		{Platform: "other", ChatType: "group", ChatID: "1", Alias: "REFIDX_same", PlatformMessageID: "third"},
	}
	for _, mapping := range mappings {
		for range 2 {
			if err := db.MapPlatformMessageAlias(ctx, mapping); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.AppendChatMessage(ctx, &ChatMessage{
		Platform: "qqofficial", ChatType: "group", ChatID: "1", PlatformMessageID: "first", SenderID: "user", Text: "old", CreatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openHistoryForTest(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := db
	for _, mapping := range mappings {
		got, sender, err := repo.ResolvePlatformMessageAlias(ctx, mapping.Platform, mapping.ChatType, mapping.ChatID, mapping.Alias)
		if err != nil || got != mapping.PlatformMessageID || sender != mapping.SenderID {
			t.Fatalf("resolve %+v: %q, %v", mapping, got, err)
		}
	}
	if _, _, err := repo.ResolvePlatformMessageAlias(ctx, "qqofficial", "private", "1", "REFIDX_same"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing alias: %v", err)
	}
	deleted, err := repo.DeleteHistoryBefore(ctx, cutoff)
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup: count=%d err=%v", deleted, err)
	}
	if _, _, err := repo.ResolvePlatformMessageAlias(ctx, "qqofficial", "group", "1", "REFIDX_same"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired alias: %v", err)
	}
	for _, mapping := range mappings[1:] {
		if got, sender, err := repo.ResolvePlatformMessageAlias(ctx, mapping.Platform, mapping.ChatType, mapping.ChatID, mapping.Alias); err != nil || got != mapping.PlatformMessageID || sender != mapping.SenderID {
			t.Fatalf("retained alias: %q, %v", got, err)
		}
	}
}
