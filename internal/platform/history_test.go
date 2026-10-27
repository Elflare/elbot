package platform

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"elbot/internal/storage"
)

func TestChatHistoryMessageProjection(t *testing.T) {
	row := storage.ChatMessage{
		ID: "history", Seq: 19, Platform: "qqofficial", PlatformScopeID: "c2c:openid", PlatformMessageID: "message",
		SenderID: "openid", SenderName: "小娅", Text: "正文", CreatedAt: time.Unix(1700000000, 0),
		ReplyToPlatformMessageID: "reply", Metadata: `{"reply":{"sender_id":"author","sender_name":"作者","text":"引用正文"}}`,
		Segments: `[{"type":"text","text":"说明"},{"type":"image","platform_file_id":"photo","name":"photo.png","size":23},{"type":"at","user_id":"someone"},{"type":"file","platform_file_id":"file","mime_type":"text/plain"}]`,
	}
	got := chatHistoryMessage(row)
	if got.ID != row.ID || got.Platform != row.Platform || got.ScopeID != row.PlatformScopeID || got.MessageID != row.PlatformMessageID || got.SenderID != row.SenderID || got.Text != row.Text || got.CreatedAt != row.CreatedAt {
		t.Fatalf("message=%+v", got)
	}
	if got.Reply.MessageID != "reply" || got.Reply.SenderID != "author" || got.Reply.SenderName != "作者" || got.Reply.Text != "引用正文" {
		t.Fatalf("reply=%+v", got.Reply)
	}
	if len(got.Media) != 2 || got.Media[0].Type != "image" || got.Media[0].PlatformFileID != "photo" || got.Media[0].Size != 23 || got.Media[1].Type != "file" || got.Media[1].PlatformFileID != "file" {
		t.Fatalf("media=%+v", got.Media)
	}
	got.Media[0].Name = "changed"
	if next := chatHistoryMessage(row); next.Media[0].Name != "photo.png" {
		t.Fatal("projection shared mutable media")
	}
}

func TestChatHistoryErrorBoundary(t *testing.T) {
	got := chatHistoryError(fmt.Errorf("lookup: %w", storage.ErrNotFound))
	if !errors.Is(got, ErrChatHistoryNotFound) || errors.Is(got, storage.ErrNotFound) {
		t.Fatalf("error=%v", got)
	}
	want := errors.New("database unavailable")
	if chatHistoryError(want) != want {
		t.Fatal("independent error lost")
	}
}
