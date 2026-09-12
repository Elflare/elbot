package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"elbot/internal/platform"
	"elbot/internal/storage"
)

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
