package qqofficial

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type captureHandler struct {
	text string
	ctx  context.Context
}

func (h *captureHandler) HandleMessage(ctx context.Context, text string) error {
	h.ctx = ctx
	h.text = text
	return nil
}

func TestHandleC2CMessageAddsFallbackReferenceText(t *testing.T) {
	adapter := New(Config{}, nil, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, c2cMessage{
		ID:      "msg-1",
		Author:  c2cAuthor{UserOpenID: "user-1"},
		Content: "你看看有没",
		MessageReference: &messageReference{
			MessageID: "notice-1",
			Content:   "已保存附件：attachment-1\n路径：/tmp/attachment-1",
		},
	})

	want := "[引用]：已保存附件：attachment-1\n路径：/tmp/attachment-1\n\n你看看有没"
	if handler.text != want {
		t.Fatalf("text = %q, want %q", handler.text, want)
	}
}

func TestHandleC2CMessageForksOwnOlderAssistantReference(t *testing.T) {
	ctx := context.Background()
	store := newQQOfficialTestStore(t)
	adapter := New(Config{}, store, nil)
	svc := session.NewService(store)
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: platformName, PlatformScopeID: "c2c:user-1"}
	s, err := svc.Create(ctx, scope, "source")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	first := &storage.Message{ID: storage.NewID(), SessionID: s.ID, Role: storage.RoleAssistant, Content: "old answer"}
	latest := &storage.Message{ID: storage.NewID(), SessionID: s.ID, Role: storage.RoleAssistant, Content: "latest answer"}
	if err := store.Messages().Append(ctx, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := store.Messages().Append(ctx, latest); err != nil {
		t.Fatalf("append latest: %v", err)
	}
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: platformName, PlatformScopeID: scope.PlatformScopeID, PlatformMessageID: "platform-old", MessageID: first.ID, SessionID: s.ID}); err != nil {
		t.Fatalf("map first: %v", err)
	}

	handler := &captureHandler{}
	adapter.handleC2CMessage(ctx, handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, c2cMessage{
		ID:               "msg-1",
		Author:           c2cAuthor{UserOpenID: "user-1"},
		Content:          "继续",
		MessageReference: &messageReference{MessageID: "platform-old"},
	})
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ForkFromMessageID != first.ID {
		t.Fatalf("fork = %q, want %q", msgCtx.ForkFromMessageID, first.ID)
	}
	if handler.text != "继续" {
		t.Fatalf("text = %q, want original", handler.text)
	}
}

func TestHandleC2CMessageImageAttachmentReachesHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake png data"))
	}))
	defer server.Close()

	adapter := New(Config{}, nil, nil)
	adapter.client.http = server.Client()
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, c2cMessage{
		ID:     "msg-1",
		Author: c2cAuthor{UserOpenID: "user-1"},
		Attachments: []messageAttachment{{
			URL:         server.URL + "/image",
			ContentType: "image/png",
			Filename:    "image.jpg",
			Width:       465,
			Height:      600,
		}},
	})

	if handler.text != "" {
		t.Fatalf("text = %q, want empty", handler.text)
	}
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if len(msgCtx.Segments) != 1 {
		t.Fatalf("segments len = %d, want 1", len(msgCtx.Segments))
	}
	image := msgCtx.Segments[0]
	if image.Type != platform.SegmentImage {
		t.Fatalf("segment type = %q, want image", image.Type)
	}
	if image.MIMEType != "image/png" {
		t.Fatalf("mime = %q, want image/png", image.MIMEType)
	}
	if !strings.HasPrefix(image.URL, "data:image/png;base64,") {
		t.Fatalf("image URL = %q, want data URL", image.URL)
	}
}

func TestHandleC2CMessageStickerAttachmentStripsFaceFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake jpeg data"))
	}))
	defer server.Close()

	adapter := New(Config{}, nil, nil)
	adapter.client.http = server.Client()
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, c2cMessage{
		ID:      "msg-1",
		Author:  c2cAuthor{UserOpenID: "user-1"},
		Content: `<faceType=6,faceId="0",ext="eyJ0ZXh0IjoiIn0=">`,
		Attachments: []messageAttachment{{
			URL:         server.URL + "/sticker",
			ContentType: "image/jpeg",
			Filename:    "sticker.jpg",
			Width:       55,
			Height:      56,
		}},
	})

	if handler.text != "" {
		t.Fatalf("text = %q, want empty", handler.text)
	}
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if len(msgCtx.Segments) != 1 || msgCtx.Segments[0].Type != platform.SegmentImage {
		t.Fatalf("segments = %#v, want image only", msgCtx.Segments)
	}
}

func TestHandleC2CMessageTextAndStickerStripsOnlyFaceFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake jpeg data"))
	}))
	defer server.Close()

	adapter := New(Config{}, nil, nil)
	adapter.client.http = server.Client()
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, c2cMessage{
		ID:      "msg-1",
		Author:  c2cAuthor{UserOpenID: "user-1"},
		Content: `看看这个<faceType=6,faceId="0",ext="eyJ0ZXh0IjoiIn0=">`,
		Attachments: []messageAttachment{{
			URL:         server.URL + "/sticker",
			ContentType: "image/jpeg",
			Filename:    "sticker.jpg",
			Width:       55,
			Height:      56,
		}},
	})

	if handler.text != "看看这个" {
		t.Fatalf("text = %q, want cleaned text", handler.text)
	}
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if len(msgCtx.Segments) != 2 || msgCtx.Segments[0].Text != "看看这个" || msgCtx.Segments[1].Type != platform.SegmentImage {
		t.Fatalf("segments = %#v, want text + image", msgCtx.Segments)
	}
}

func TestPrepareInboundAttachmentsSavesFileAttachment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("test file"))
	}))
	defer server.Close()

	adapter := New(Config{AttachmentDir: t.TempDir()}, nil, nil)
	adapter.client.http = server.Client()
	prepared := adapter.prepareInboundAttachments(context.Background(), []messageAttachment{{
		URL:         server.URL + "/file",
		ContentType: "file",
		Filename:    "test.txt",
		Size:        9,
	}})

	if len(prepared.Saved) != 1 {
		t.Fatalf("saved len = %d, want 1", len(prepared.Saved))
	}
	if filepath.Base(prepared.Saved[0].Path) != "test.txt" {
		t.Fatalf("saved path = %q, want test.txt", prepared.Saved[0].Path)
	}
	data, err := os.ReadFile(prepared.Saved[0].Path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if string(data) != "test file" {
		t.Fatalf("saved data = %q, want test file", string(data))
	}
	if len(prepared.Segments) != 1 || prepared.Segments[0].Type != platform.SegmentFile {
		t.Fatalf("segments = %#v, want file segment", prepared.Segments)
	}
}

func TestHandleC2CMessageContinuesLatestAssistantReference(t *testing.T) {
	ctx := context.Background()
	store := newQQOfficialTestStore(t)
	adapter := New(Config{}, store, nil)
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: platformName, PlatformScopeID: "c2c:user-1"}
	_, latest := createQQOfficialAssistantMessages(t, ctx, store, scope)
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: platformName, PlatformScopeID: scope.PlatformScopeID, PlatformMessageID: "platform-latest", MessageID: latest.ID, SessionID: latest.SessionID}); err != nil {
		t.Fatalf("map latest: %v", err)
	}

	handler := &captureHandler{}
	adapter.handleC2CMessage(ctx, handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, c2cMessage{
		ID:               "msg-1",
		Author:           c2cAuthor{UserOpenID: "user-1"},
		Content:          "继续",
		MessageReference: &messageReference{MessageID: "platform-latest"},
	})
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ForkFromMessageID != "" {
		t.Fatalf("fork = %q, want empty", msgCtx.ForkFromMessageID)
	}
	if handler.text != "继续" {
		t.Fatalf("text = %q, want original", handler.text)
	}
}

func createQQOfficialAssistantMessages(t *testing.T, ctx context.Context, store storage.Store, scope session.Scope) (*storage.Message, *storage.Message) {
	t.Helper()
	s, err := session.NewService(store).Create(ctx, scope, "source")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	first := &storage.Message{ID: storage.NewID(), SessionID: s.ID, Role: storage.RoleAssistant, Content: "old answer"}
	latest := &storage.Message{ID: storage.NewID(), SessionID: s.ID, Role: storage.RoleAssistant, Content: "latest answer"}
	if err := store.Messages().Append(ctx, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := store.Messages().Append(ctx, latest); err != nil {
		t.Fatalf("append latest: %v", err)
	}
	return first, latest
}

func newQQOfficialTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
