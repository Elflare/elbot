package historydb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"elbot/internal/storage/share"
)

func TestChatHistoryAddsMissingSegmentsAndPreservesMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	old, err := share.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(strings.Replace(legacyChatHistorySQL, "    segments TEXT NULL,", "", 1)); err != nil {
		t.Fatal(err)
	}
	message := ChatMessage{ID: "old-id", Text: "keep text"}
	if _, err = old.Exec(`INSERT INTO chat_messages(id,platform,platform_scope_id,scope_type,platform_message_id,sender_id,text,created_at) VALUES('old-id','cli','local','cli','old','user','keep text',?)`, share.FormatTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store, err := Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer store.Close()
			got, err := store.GetByPlatformMessage(t.Context(), "cli", "private", "local", "old")
			if err != nil || got.Text != message.Text || got.ID != message.ID {
				t.Fatalf("existing message changed: %+v, %v", got, err)
			}
			media := &ChatMessage{Platform: "cli", ChatID: "local", PlatformMessageID: "media", SenderID: "user", Segments: `[{"type":"image","name":"image.png"}]`, ChatType: "private"}
			if err := store.AppendChatMessage(t.Context(), media); err != nil {
				t.Fatal(err)
			}
			var segments sql.NullString
			if err := store.history.QueryRow(`SELECT segments FROM chat_messages WHERE platform_message_id = 'media'`).Scan(&segments); err != nil || segments.String != media.Segments {
				t.Fatalf("segments = %v, error = %v", segments, err)
			}
		}()
	}
}

func TestAssistantHistoryVisibility(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := store
	for i, id := range []string{"one", "first reply", "two", "second reply", "three"} {
		if err := repo.AppendChatMessage(ctx, &ChatMessage{Platform: "p", ChatType: "group", ChatID: "c", PlatformMessageID: id, Text: id, IsAssistant: i%2 != 0}); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(rows []ChatMessage, err error, want ...string) {
		t.Helper()
		var ids []string
		for _, row := range rows {
			ids = append(ids, row.PlatformMessageID)
		}
		if err != nil || !slices.Equal(ids, want) {
			t.Fatalf("got %v (%v), want %v", ids, err, want)
		}
	}
	q := ChatHistorySearchRequest{Platform: "p", ChatType: "group", ChatID: "c", Limit: 3}
	rows, err := repo.SearchChatHistory(ctx, q)
	expect(rows, err, "one", "two", "three")
	center := rows[1].Seq
	q.IncludeAssistant = true
	rows, err = repo.SearchChatHistory(ctx, q)
	expect(rows, err, "two", "second reply", "three")
	assistant := rows[1].Seq
	window := ChatHistoryAroundRequest{Platform: "p", ChatType: "group", ChatID: "c", Seq: center, Before: 1, After: 1}
	rows, err = repo.ChatHistoryAround(ctx, window)
	expect(rows, err, "one", "two", "three")
	window.IncludeAssistant = true
	rows, err = repo.ChatHistoryAround(ctx, window)
	expect(rows, err, "first reply", "two", "second reply")
	window.Seq, window.Before, window.After = assistant, 0, 0
	rows, err = repo.ChatHistoryAround(ctx, window)
	expect(rows, err, "second reply")
	if !rows[0].IsAssistant {
		t.Fatal("missing assistant role")
	}
	window.IncludeAssistant = false
	if _, err = repo.ChatHistoryAround(ctx, window); !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("hidden target: %v", err)
	}
}

func TestChatHistoryQueriesIncludeMediaOnlyMessages(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("NewChatHistory: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := store

	messages := []*ChatMessage{
		{Platform: "qqonebot", ChatType: "group", ChatID: "1", PlatformMessageID: "text", SenderID: "user", Text: "hello"},
		{Platform: "qqonebot", ChatType: "group", ChatID: "1", PlatformMessageID: "media", SenderID: "user", Segments: `[{"type":"image","name":"sticker.jpg"}]`},
		{Platform: "qqonebot", ChatType: "group", ChatID: "1", PlatformMessageID: "empty", SenderID: "user"},
	}
	for _, message := range messages {
		if err := repo.AppendChatMessage(ctx, message); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := repo.SearchChatHistory(ctx, ChatHistorySearchRequest{Platform: "qqonebot", ChatType: "group", ChatID: "1", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 || got[0].PlatformMessageID != "text" || got[1].PlatformMessageID != "media" {
		t.Fatalf("Search messages = %#v", got)
	}

	got, err = repo.ChatHistoryAround(ctx, ChatHistoryAroundRequest{Platform: "qqonebot", ChatType: "group", ChatID: "1", Seq: got[0].Seq, After: 10})
	if err != nil {
		t.Fatalf("Around: %v", err)
	}
	if len(got) != 2 || got[0].PlatformMessageID != "text" || got[1].PlatformMessageID != "media" {
		t.Fatalf("Around messages = %#v", got)
	}
}

func TestChatHistorySearchMatchesIDOrNameWithinScope(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := store
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	rows := []ChatMessage{
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
		rows[i].Platform, rows[i].ChatID, rows[i].ChatType = "qqofficial", "AB12_OPENID", "private"
		rows[i].Text, rows[i].CreatedAt = "needle", now
		if err := repo.AppendChatMessage(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	// 同名发送者在其他平台、聊天、时间与正文中均不得通过 OR 分支混入。
	for i, other := range []ChatMessage{
		{Platform: "telegram", ChatType: "private", ChatID: "AB12_OPENID", Text: "needle", CreatedAt: now},
		{Platform: "qqofficial", ChatType: "group", ChatID: "other", Text: "needle", CreatedAt: now},
		{Platform: "qqofficial", ChatType: "private", ChatID: "AB12_OPENID", Text: "unrelated", CreatedAt: now},
		{Platform: "qqofficial", ChatType: "private", ChatID: "AB12_OPENID", Text: "needle", CreatedAt: now.Add(-24 * time.Hour)},
	} {
		other.PlatformMessageID = string(rune('a' + i))
		other.SenderID, other.SenderName = "AB12_OPENID", "AB12_OPENID"
		if err := repo.AppendChatMessage(ctx, &other); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := repo.GetByPlatformMessage(ctx, "qqofficial", "private", "AB12_OPENID", "openid")
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
			got, err := repo.SearchChatHistory(ctx, ChatHistorySearchRequest{Platform: "qqofficial", ChatType: "private", ChatID: "AB12_OPENID", SenderQuery: tc.user, QueryTerms: []string{"needle"}, Since: &since, Limit: 50})
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
