package share

import (
	"elbot/internal/storage"
	"testing"
)

func TestChatMetadataPreservesReferenceSnapshot(t *testing.T) {
	want := ReplyContext{MessageID: "42", SenderID: "1001", SenderName: "小娅", Text: "原消息"}
	message := storage.ChatMessage{ReplyToPlatformMessageID: want.MessageID, Metadata: MarshalChatMetadata(want)}
	got := ChatMessageReply(message)
	if got.MessageID != want.MessageID || got.SenderID != want.SenderID || got.SenderName != want.SenderName || got.Text != want.Text {
		t.Fatalf("reply=%+v want=%+v", got, want)
	}
	message.Metadata = `{"reply":{"sender_id":"old","sender_name":"旧昵称","text":"旧正文"}}`
	if got := ChatMessageReply(message); got.SenderID != "old" || got.Text != "旧正文" {
		t.Fatalf("stored format changed: %+v", got)
	}
	message.Metadata = "invalid"
	if got := ChatMessageReply(message); got.MessageID != "42" || got.Text != "" {
		t.Fatalf("bad metadata lost message ID: %+v", got)
	}
}
