package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

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
		{Platform: "qqonebot", PlatformScopeID: "group:1", PlatformMessageID: "media", SenderID: "user", Segments: `[{"type":"image","name":"sticker.jpg"}]`},
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

func TestChatHistorySearchMatchesIDOrNameWithinScope(t *testing.T) {
	ctx := t.Context()
	store, err := NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := store.Repository()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	rows := []storage.ChatMessage{
		{PlatformMessageID: "openid", SenderID: "AB12_OPENID"},
		{PlatformMessageID: "name", SenderID: "other", SenderName: "prefix AB12_OPENID suffix"},
		{PlatformMessageID: "partial-id", SenderID: "prefix_AB12_OPENID"},
		{PlatformMessageID: "numeric-id", SenderID: "123"},
		{PlatformMessageID: "numeric-name", SenderID: "456", SenderName: "123"},
		{PlatformMessageID: "chinese", SenderID: "cn", SenderName: "小芙蕾娅"},
		{PlatformMessageID: "english", SenderID: "en", SenderName: "Alice"},
		{PlatformMessageID: "literal-at", SenderID: "@AB12_OPENID"},
		{PlatformMessageID: "literal-self", SenderID: "self", SenderName: "我"},
		{PlatformMessageID: "both", SenderID: "same", SenderName: "same"},
		{PlatformMessageID: "wildcard-underscore", SenderID: "unrelated", SenderName: "AB12XOPENID"},
		{PlatformMessageID: "percent-name", SenderID: "percent", SenderName: "prefix a%b suffix"},
		{PlatformMessageID: "percent-id", SenderID: "a%b"},
		{PlatformMessageID: "percent-decoy", SenderID: "decoy", SenderName: "aXXb"},
		{PlatformMessageID: "escape-name", SenderID: "escape", SenderName: "prefix a!b suffix"},
		{PlatformMessageID: "slash-name", SenderID: "slash", SenderName: `prefix a\b suffix`},
	}
	for i := range rows {
		rows[i].Platform, rows[i].PlatformScopeID, rows[i].ScopeType = "qqofficial", "c2c:AB12_OPENID", "private"
		rows[i].Text, rows[i].CreatedAt = "needle", now
		if err := repo.Append(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	// 同名发送者在其他平台、聊天、时间与正文中均不得通过 OR 分支混入。
	for i, other := range []storage.ChatMessage{
		{Platform: "telegram", PlatformScopeID: "c2c:AB12_OPENID", Text: "needle", CreatedAt: now},
		{Platform: "qqofficial", PlatformScopeID: "group:other", Text: "needle", CreatedAt: now},
		{Platform: "qqofficial", PlatformScopeID: "c2c:AB12_OPENID", Text: "unrelated", CreatedAt: now},
		{Platform: "qqofficial", PlatformScopeID: "c2c:AB12_OPENID", Text: "needle", CreatedAt: now.Add(-24 * time.Hour)},
	} {
		other.PlatformMessageID = string(rune('a' + i))
		other.SenderID, other.SenderName = "AB12_OPENID", "AB12_OPENID"
		if err := repo.Append(ctx, &other); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := repo.GetByPlatformMessage(ctx, "qqofficial", "c2c:AB12_OPENID", "openid")
	if err != nil || stored.SenderID != "AB12_OPENID" || stored.SenderName != "" {
		t.Fatalf("stored C2C row=%+v error=%v", stored, err)
	}
	since := now.Add(-time.Hour)
	for _, tc := range []struct {
		user string
		want []string
	}{
		{"AB12_OPENID", []string{"name", "openid"}},
		{"123", []string{"numeric-id", "numeric-name"}},
		{"芙蕾", []string{"chinese"}},
		{"Ali", []string{"english"}},
		{"@AB12_OPENID", []string{"literal-at"}},
		{"我", []string{"literal-self"}},
		{"same", []string{"both"}},
		{"a%b", []string{"percent-id", "percent-name"}},
		{"a!b", []string{"escape-name"}},
		{`a\b`, []string{"slash-name"}},
	} {
		t.Run(tc.user, func(t *testing.T) {
			got, err := repo.Search(ctx, storage.ChatHistorySearchRequest{Platform: "qqofficial", PlatformScopeID: "c2c:AB12_OPENID", SenderQuery: tc.user, QueryTerms: []string{"needle"}, Since: &since, Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, row := range got {
				ids = append(ids, row.PlatformMessageID)
			}
			slices.Sort(ids)
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("ids=%v want=%v", ids, tc.want)
			}
		})
	}
}
