package qqofficial

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"elbot/internal/platform"
	"elbot/internal/storage/sqlite"
)

func TestPureMediaHistoryWithoutDownload(t *testing.T) {
	ctx := context.Background()
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("ordinary group media must not download") }))
	defer server.Close()
	a := New(Config{}, nil, history.Repository(), nil)
	handler := &captureHandler{}
	image := messageAttachment{URL: server.URL + "/image.png", ContentType: "image/png", Filename: "image.png"}
	a.handleGroupMessage(ctx, handler, payload{Type: eventGroupMessageCreate}, inboundMessage{
		ID: "1", GroupOpenID: "9", Author: inboundAuthor{MemberOpenID: "1"},
		Attachments: []messageAttachment{image, {URL: server.URL + "/file", ContentType: "file", Filename: "file.txt", Size: 5}, image},
	})
	row, err := history.Repository().GetByPlatformMessage(ctx, platformName, "group:9", "1")
	if err != nil {
		t.Fatal(err)
	}
	segments := platform.UnmarshalChatSegments(row.Segments)
	if len(segments) != 3 || segments[0].Type != platform.SegmentImage || segments[1].Type != platform.SegmentFile || segments[2] != segments[0] {
		t.Fatalf("ordered history = %#v", segments)
	}
	msg, ok := platform.MessageContextFrom(handler.ctx)
	if !ok || len(msg.Segments) != 3 || msg.Segments[1].Name != "file.txt" {
		t.Fatalf("handler media = %#v", msg)
	}
}
