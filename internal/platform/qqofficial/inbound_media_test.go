// inbound_media_test.go 验证纯媒体入站历史的内容与片段顺序。

package qqofficial

import (
	"context"
	"path/filepath"
	"testing"

	"elbot/internal/platform/share"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestPureMediaHistoryPreservesOrder(t *testing.T) {
	ctx := context.Background()
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	a := newTestAdapter(t, Config{}, history.Repository())
	handler := &captureHandler{}
	image := messageAttachment{URL: "https://example.com/image.png", ContentType: "image/png", Filename: "image.png"}
	a.handleGroupMessage(ctx, handler.HandleMessage, payload{Type: eventGroupMessageCreate}, inboundMessage{
		ID: "1", GroupOpenID: "9", Author: inboundAuthor{MemberOpenID: "1"},
		Attachments: []messageAttachment{image, {URL: "https://example.com/file", ContentType: "file", Filename: "file.txt", Size: 5}, image},
	})
	row, err := history.Repository().GetByPlatformMessage(ctx, platformName, "group:9", "1")
	if err != nil {
		t.Fatal(err)
	}
	segments := storage.UnmarshalChatSegments(row.Segments)
	if len(segments) != 3 || segments[0].Type != "image" || segments[1].Type != "file" || segments[2] != segments[0] {
		t.Fatalf("ordered history = %#v", segments)
	}
	msg, ok := share.MessageContextFrom(handler.ctx)
	if !ok || len(msg.Segments) != 3 || msg.Segments[1].Name != "file.txt" {
		t.Fatalf("handler media = %#v", msg)
	}
}
