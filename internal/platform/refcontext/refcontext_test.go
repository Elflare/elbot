package refcontext

import (
	"context"
	"path/filepath"
	"testing"

	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestApplyFallsBackToReferencedText(t *testing.T) {
	result := Apply(context.Background(), Options{
		ReplyID: "notice-1",
		Text:    "说：收到",
		Fetch: func(context.Context, string) (ReferencedMessage, bool) {
			return ReferencedMessage{Label: "引用：通知", Text: "已保存附件：attachment-1", Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "已保存附件：attachment-1"}}}, true
		},
	})
	want := "[引用：通知]：已保存附件：attachment-1\n\n说：收到"
	if result.Text != want {
		t.Fatalf("text = %q, want %q", result.Text, want)
	}
	if result.ForkFromMessageID != "" {
		t.Fatalf("fork = %q, want empty", result.ForkFromMessageID)
	}
}

func TestApplyForksOwnOlderAssistantReference(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	svc := session.NewService(store)
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: "qqofficial", PlatformScopeID: "c2c:user-1"}
	s, err := svc.Create(ctx, scope, "source")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	first := &storage.Message{ID: storage.NewID(), SessionID: s.ID, Role: storage.RoleAssistant, Content: "old"}
	latest := &storage.Message{ID: storage.NewID(), SessionID: s.ID, Role: storage.RoleAssistant, Content: "latest"}
	if err := store.Messages().Append(ctx, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := store.Messages().Append(ctx, latest); err != nil {
		t.Fatalf("append latest: %v", err)
	}
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: "qqofficial", PlatformScopeID: scope.PlatformScopeID, PlatformMessageID: "p-old", MessageID: first.ID, SessionID: s.ID}); err != nil {
		t.Fatalf("map first: %v", err)
	}

	result := Apply(ctx, Options{Store: store, Platform: "qqofficial", ScopeID: scope.PlatformScopeID, ActorID: scope.ActorID, ReplyID: "p-old", Text: "继续"})
	if result.ForkFromMessageID != first.ID {
		t.Fatalf("fork = %q, want %q", result.ForkFromMessageID, first.ID)
	}
	if result.Text != "继续" {
		t.Fatalf("text = %q, want original", result.Text)
	}
}

func newRefTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
