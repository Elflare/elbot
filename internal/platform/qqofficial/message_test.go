package qqofficial

import (
	"context"
	"path/filepath"
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

func newQQOfficialTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
