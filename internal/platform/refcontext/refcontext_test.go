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
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: "qqofficial", PlatformScopeID: "c2c:user-1"}
	first, _ := createAssistantMessages(t, ctx, store, scope)
	mapPlatformMessage(t, ctx, store, scope, "p-old", first)

	result := Apply(ctx, Options{Store: store, Platform: "qqofficial", ScopeID: scope.PlatformScopeID, ActorID: scope.ActorID, ReplyID: "p-old", Text: "继续"})
	if result.ForkFromMessageID != first.ID {
		t.Fatalf("fork = %q, want %q", result.ForkFromMessageID, first.ID)
	}
	if result.Text != "继续" {
		t.Fatalf("text = %q, want original", result.Text)
	}
}

func TestApplyLatestOwnAssistantReferenceContinues(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: "qqofficial", PlatformScopeID: "c2c:user-1"}
	_, latest := createAssistantMessages(t, ctx, store, scope)
	mapPlatformMessage(t, ctx, store, scope, "p-latest", latest)

	result := Apply(ctx, Options{Store: store, Platform: "qqofficial", ScopeID: scope.PlatformScopeID, ActorID: scope.ActorID, ReplyID: "p-latest", Text: "继续"})
	if result.ForkFromMessageID != "" {
		t.Fatalf("fork = %q, want empty", result.ForkFromMessageID)
	}
	if result.Text != "继续" {
		t.Fatalf("text = %q, want original", result.Text)
	}
}

func TestApplySuperadminBackgroundReferenceResumes(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: "qqofficial", PlatformScopeID: "c2c:user-1"}
	bg := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: "elnis:event-1", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "elnis"}
	if err := store.Sessions().Create(ctx, bg); err != nil {
		t.Fatalf("create background session: %v", err)
	}
	msg := &storage.Message{ID: storage.NewID(), SessionID: bg.ID, Role: storage.RoleAssistant, Content: "report"}
	if err := store.Messages().Append(ctx, msg); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	mapPlatformMessage(t, ctx, store, scope, "p-bg", msg)

	result := Apply(ctx, Options{Store: store, Platform: scope.Platform, ScopeID: scope.PlatformScopeID, ActorID: scope.ActorID, IsSuperadmin: true, ReplyID: "p-bg", Text: "继续"})
	if result.ResumeSessionID != bg.ID {
		t.Fatalf("resume = %q, want %q", result.ResumeSessionID, bg.ID)
	}
	if result.ForkFromMessageID != "" || result.Text != "继续" {
		t.Fatalf("result = %#v", result)
	}
}

func TestApplyUserBackgroundReferenceFallsBack(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: "qqofficial", PlatformScopeID: "c2c:user-1"}
	bg := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: "cron:user.cron.test", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "cron"}
	if err := store.Sessions().Create(ctx, bg); err != nil {
		t.Fatalf("create background session: %v", err)
	}
	msg := &storage.Message{ID: storage.NewID(), SessionID: bg.ID, Role: storage.RoleAssistant, Content: "report"}
	if err := store.Messages().Append(ctx, msg); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	mapPlatformMessage(t, ctx, store, scope, "p-bg-user", msg)

	result := Apply(ctx, Options{Store: store, Platform: scope.Platform, ScopeID: scope.PlatformScopeID, ActorID: scope.ActorID, ReplyID: "p-bg-user", Text: "继续"})
	want := "[引用：bot]：report\n\n继续"
	if result.Text != want {
		t.Fatalf("text = %q, want %q", result.Text, want)
	}
	if result.ResumeSessionID != "" || result.ForkFromMessageID != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestApplyOtherSessionAssistantReferenceFallsBack(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	otherScope := session.Scope{ActorID: "qqofficial:user-2", Platform: "qqofficial", PlatformScopeID: "c2c:user-1"}
	msg, _ := createAssistantMessages(t, ctx, store, otherScope)
	mapPlatformMessage(t, ctx, store, otherScope, "p-other", msg)

	result := Apply(ctx, Options{Store: store, Platform: "qqofficial", ScopeID: otherScope.PlatformScopeID, ActorID: "qqofficial:user-1", ReplyID: "p-other", Text: "继续"})
	want := "[引用：bot]：old\n\n继续"
	if result.Text != want {
		t.Fatalf("text = %q, want %q", result.Text, want)
	}
	if result.ForkFromMessageID != "" {
		t.Fatalf("fork = %q, want empty", result.ForkFromMessageID)
	}
}

func TestApplyForkCommandUsesReferencedAssistantID(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: "qqofficial", PlatformScopeID: "c2c:user-1"}
	msg, _ := createAssistantMessages(t, ctx, store, scope)
	mapPlatformMessage(t, ctx, store, scope, "p-old", msg)

	result := Apply(ctx, Options{Store: store, Platform: "qqofficial", ScopeID: scope.PlatformScopeID, ActorID: scope.ActorID, ReplyID: "p-old", Text: "/fork", CommandPrefixes: []string{"/"}})
	want := "/fork " + msg.ID
	if result.Text != want {
		t.Fatalf("text = %q, want %q", result.Text, want)
	}
}

func createAssistantMessages(t *testing.T, ctx context.Context, store storage.Store, scope session.Scope) (*storage.Message, *storage.Message) {
	t.Helper()
	s, err := session.NewService(store).Create(ctx, scope, session.CreateRequest{Title: "source"})
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
	return first, latest
}

func mapPlatformMessage(t *testing.T, ctx context.Context, store storage.Store, scope session.Scope, platformMessageID string, msg *storage.Message) {
	t.Helper()
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID, PlatformMessageID: platformMessageID, MessageID: msg.ID, SessionID: msg.SessionID}); err != nil {
		t.Fatalf("map message: %v", err)
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
