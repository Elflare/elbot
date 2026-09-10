package qqonebot

import (
	"context"
	"elbot/internal/media"
	"elbot/internal/storage"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/platform"
	"elbot/internal/storage/sqlite"
)

func TestPureMediaHistoryPreservesOrderWithoutCredentials(t *testing.T) {
	ctx := context.Background()
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	a := New(Config{}, nil, history.Repository(), nil)
	raw := `[{"type":"image","data":{"file":"image-id","url":"https://example.com/image?rkey=secret"}},{"type":"file","data":{"file_id":"file-id","name":"file.txt"}},{"type":"image","data":{"file":"image-id","url":"https://example.com/image?rkey=secret"}}]`
	handler := &captureHandler{}
	a.handleEvent(ctx, handler, Event{MessageType: "group", MessageID: 1, GroupID: 9, UserID: 1, Message: []byte(raw), RawMessage: raw})
	row, err := history.Repository().GetByPlatformMessage(ctx, "qqonebot", "group:9", "1")
	if err != nil {
		t.Fatal(err)
	}
	segments := platform.UnmarshalChatSegments(row.Segments)
	if len(segments) != 3 || segments[0].Type != platform.SegmentImage || segments[1].Type != platform.SegmentFile || segments[2] != segments[0] || segments[0].PlatformFileID != "image-id" {
		t.Fatalf("ordered history = %#v", segments)
	}
	if strings.Contains(row.Segments+row.Raw, "secret") {
		t.Fatalf("history leaked credentials: %#v", row)
	}
}

func TestOutputReferenceDoesNotFetchPlatform(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	item, err := center.ImportBytes(ctx, []byte("image"), media.Input{Name: "image.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Media().SaveOutput(ctx, storage.MediaOutput{Platform: "qqonebot", ScopeID: "group:9", MessageID: "sent", Kind: "image", MediaID: item.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a := New(Config{}, store, nil, nil)
	a.transport = newTestTransport(t, func(req request) response {
		t.Errorf("unexpected reference API: %s", req.Action)
		return response{Status: "failed", Echo: req.Echo}
	})
	handler := &captureHandler{}
	a.handleEvent(ctx, handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 2, Message: []byte(`[{"type":"reply","data":{"id":"sent"}},{"type":"text","data":{"text":"看看"}}]`)})
	msg, ok := platform.MessageContextFrom(handler.ctx)
	if !ok || msg.ReplyToSenderID != "1000" || len(msg.ContextSegments) != 2 || msg.ContextSegments[1].MediaID != item.ID {
		t.Fatalf("output reference = %#v", msg)
	}
}
