package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"elbot/internal/platform"
	"elbot/internal/storage"
)

func TestChatHistoryAddsMissingSegmentsAndPreservesMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	old, err := NewChatHistory(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	message := &storage.ChatMessage{Platform: "cli", PlatformScopeID: "local", PlatformMessageID: "old", SenderID: "user", Text: "keep text"}
	if err := old.Repository().Append(t.Context(), message); err != nil {
		_ = old.Close()
		t.Fatal(err)
	}
	// This column was absent in the original chat history database.
	if _, err := old.db.Exec(`ALTER TABLE chat_messages DROP COLUMN segments`); err != nil {
		_ = old.Close()
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store, err := NewChatHistory(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer store.Close()
			got, err := store.Repository().GetByPlatformMessage(t.Context(), "cli", "local", "old")
			if err != nil || got.Text != message.Text || got.ID != message.ID {
				t.Fatalf("existing message changed: %+v, %v", got, err)
			}
			media := &storage.ChatMessage{Platform: "cli", PlatformScopeID: "local", PlatformMessageID: "media", SenderID: "user", Segments: `[{"type":"image","name":"image.png"}]`}
			if err := store.Repository().Append(t.Context(), media); err != nil {
				t.Fatal(err)
			}
			var segments sql.NullString
			if err := store.db.QueryRow(`SELECT segments FROM chat_messages WHERE platform_message_id = 'media'`).Scan(&segments); err != nil || segments.String != media.Segments {
				t.Fatalf("segments = %v, error = %v", segments, err)
			}
		}()
	}
}

func TestChatHistoryQueriesIncludeMediaOnlyMessages(t *testing.T) {
	ctx := context.Background()
	store, err := NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("NewChatHistory: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := store.Repository()

	messages := []*storage.ChatMessage{
		{Platform: "qqonebot", PlatformScopeID: "group:1", PlatformMessageID: "text", SenderID: "user", Text: "hello"},
		{Platform: "qqonebot", PlatformScopeID: "group:1", PlatformMessageID: "media", SenderID: "user", Segments: platform.MarshalChatSegments([]platform.MessageSegment{{Type: platform.SegmentImage, Name: "sticker.jpg"}})},
		{Platform: "qqonebot", PlatformScopeID: "group:1", PlatformMessageID: "empty", SenderID: "user"},
	}
	for _, message := range messages {
		if err := repo.Append(ctx, message); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := repo.Search(ctx, storage.ChatHistorySearchRequest{Platform: "qqonebot", PlatformScopeID: "group:1", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 || got[0].PlatformMessageID != "text" || got[1].PlatformMessageID != "media" {
		t.Fatalf("Search messages = %#v", got)
	}

	got, err = repo.Around(ctx, storage.ChatHistoryAroundRequest{Platform: "qqonebot", PlatformScopeID: "group:1", PlatformMessageID: "text", After: 10})
	if err != nil {
		t.Fatalf("Around: %v", err)
	}
	if len(got) != 2 || got[0].PlatformMessageID != "text" || got[1].PlatformMessageID != "media" {
		t.Fatalf("Around messages = %#v", got)
	}
}
