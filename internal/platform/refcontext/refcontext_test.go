package refcontext

import (
	"context"
	"elbot/internal/contextinfo"
	"path/filepath"
	"testing"

	"elbot/internal/command"
	"elbot/internal/platform/share"
	"elbot/internal/session/control"
	sessionshare "elbot/internal/session/share"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type referenceTestScope struct {
	Owner            contextinfo.ActorIdentity
	Platform, ChatID string
	ChatType         contextinfo.ChatType
}

// Fixture encoding uses the storage format; refcontext consumes platform facts after decoding.
func encodeReferenceSegments(segments []share.MessageSegment) string {
	stored := make([]storage.ChatSegment, len(segments))
	for i, segment := range segments {
		stored[i] = storage.ChatSegment{Type: string(segment.Type), Text: segment.Text, UserID: segment.UserID, MediaID: segment.MediaID, URL: segment.URL, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size}
	}
	return storage.MarshalChatSegments(stored)
}

func TestApplyFallsBackToReferencedText(t *testing.T) {
	result, err := Apply(context.Background(), newRefTestOptions(newRefTestStore(t), newRefTestHistory(t)), share.ReferenceOptions{Platform: "qqonebot", ReplyID: "notice-1", Text: "说：收到", Fetch: func(context.Context, string) (share.ReferencedMessage, bool, error) {
		return share.ReferencedMessage{SenderID: "123456", SenderName: "通知", Label: "引用：通知", Text: "已保存附件：attachment-1", Segments: []share.MessageSegment{{Type: share.SegmentText, Text: "已保存附件：attachment-1"}}}, true, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "[引用#notice-1：通知(id:123456):已保存附件：attachment-1]\n\n说：收到"
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
	scope := referenceTestScope{Owner: contextinfo.ActorIdentity{Platform: "qqofficial", ID: "user-1"}, Platform: "qqofficial", ChatType: "private", ChatID: "user-1"}
	first, _ := createAssistantMessages(t, ctx, store, scope)
	mapPlatformMessage(t, ctx, store, scope, "p-old", first)

	result, err := Apply(ctx, newRefTestOptions(store, newRefTestHistory(t)), share.ReferenceOptions{Platform: "qqofficial", ChatID: scope.ChatID, Owner: scope.Owner, ReplyID: "p-old", Text: "继续", ChatType: scope.ChatType})
	if err != nil {
		t.Fatal(err)
	}
	if result.ForkFromMessageID != first.ID || result.ResumeSessionID != "" {
		t.Fatalf("fork = %q, want %q", result.ForkFromMessageID, first.ID)
	}
	if result.Text != "继续" {
		t.Fatalf("text = %q, want original", result.Text)
	}
}

func TestApplyLatestOwnAssistantReferenceResumes(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := referenceTestScope{Owner: contextinfo.ActorIdentity{Platform: "qqofficial", ID: "user-1"}, Platform: "qqofficial", ChatType: "private", ChatID: "user-1"}
	_, latest := createAssistantMessages(t, ctx, store, scope)
	mapPlatformMessage(t, ctx, store, scope, "p-latest", latest)

	result, err := Apply(ctx, newRefTestOptions(store, newRefTestHistory(t)), share.ReferenceOptions{Platform: "qqofficial", ChatID: scope.ChatID, Owner: scope.Owner, ReplyID: "p-latest", Text: "继续", ChatType: scope.ChatType})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResumeSessionID != latest.SessionID || result.ForkFromMessageID != "" {
		t.Fatalf("result = %#v, want resume %q", result, latest.SessionID)
	}
	if result.Text != "继续" || result.Reply.MessageID != "p-latest" || result.Reply.Text != "" || len(result.Reply.Segments) != 0 {
		t.Fatalf("resume reference = %#v", result)
	}
}

func TestApplySuperadminBackgroundReferenceResumes(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := referenceTestScope{Owner: contextinfo.ActorIdentity{Platform: "qqofficial", ID: "user-1"}, Platform: "qqofficial", ChatType: "private", ChatID: "user-1"}
	bg := &storage.Session{ID: storage.NewID(), Owner: scope.Owner, Platform: scope.Platform, ChatType: "elnis", ChatID: "event-1", Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive, Title: "elnis"}
	if err := store.Sessions().Create(ctx, bg); err != nil {
		t.Fatalf("create background session: %v", err)
	}
	msg := &storage.Message{ID: storage.NewID(), SessionID: bg.ID, Role: storage.RoleAssistant, Content: "report"}
	if err := store.Messages().Append(ctx, msg); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	mapPlatformMessage(t, ctx, store, scope, "p-bg", msg)

	result, err := Apply(ctx, newRefTestOptions(store, newRefTestHistory(t)), share.ReferenceOptions{Platform: scope.Platform, ChatID: scope.ChatID, Owner: contextinfo.ActorIdentity{Platform: scope.Platform, ID: "admin"}, ReplyID: "p-bg", Text: "继续", ChatType: scope.ChatType})
	if err != nil {
		t.Fatal(err)
	}
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
	scope := referenceTestScope{Owner: contextinfo.ActorIdentity{Platform: "qqofficial", ID: "user-1"}, Platform: "qqofficial", ChatType: "private", ChatID: "user-1"}
	bg := &storage.Session{ID: storage.NewID(), Owner: scope.Owner, Platform: scope.Platform, ChatType: "cron", ChatID: "user.cron.test", Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive, Title: "cron"}
	if err := store.Sessions().Create(ctx, bg); err != nil {
		t.Fatalf("create background session: %v", err)
	}
	msg := &storage.Message{ID: storage.NewID(), SessionID: bg.ID, Role: storage.RoleAssistant, Content: "report"}
	if err := store.Messages().Append(ctx, msg); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	mapPlatformMessage(t, ctx, store, scope, "p-bg-user", msg)

	result, err := Apply(ctx, newRefTestOptions(store, newRefTestHistory(t)), share.ReferenceOptions{Platform: scope.Platform, ChatID: scope.ChatID, Owner: scope.Owner, ReplyID: "p-bg-user", Text: "继续", ChatType: scope.ChatType})
	if err != nil {
		t.Fatal(err)
	}
	want := "[引用#p-bg-user：bot:report]\n\n继续"
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
	otherScope := referenceTestScope{Owner: contextinfo.ActorIdentity{Platform: "qqofficial", ID: "user-2"}, Platform: "qqofficial", ChatType: "private", ChatID: "user-1"}
	msg, _ := createAssistantMessages(t, ctx, store, otherScope)
	mapPlatformMessage(t, ctx, store, otherScope, "p-other", msg)

	result, err := Apply(ctx, newRefTestOptions(store, newRefTestHistory(t)), share.ReferenceOptions{Platform: "qqofficial", ChatID: otherScope.ChatID, Owner: contextinfo.ActorIdentity{Platform: "qqofficial", ID: "user-1"}, ReplyID: "p-other", Text: "继续", ChatType: otherScope.ChatType})
	if err != nil {
		t.Fatal(err)
	}
	want := "[引用#p-other：bot:old]\n\n继续"
	if result.Text != want {
		t.Fatalf("text = %q, want %q", result.Text, want)
	}
	if result.ForkFromMessageID != "" {
		t.Fatalf("fork = %q, want empty", result.ForkFromMessageID)
	}
}

func TestApplyOtherScopeAssistantReferenceFallsBack(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	currentScope := referenceTestScope{Owner: contextinfo.ActorIdentity{Platform: "qqofficial", ID: "user-1"}, Platform: "qqofficial", ChatType: "private", ChatID: "user-1"}
	otherScope := currentScope
	otherScope.ChatID = "c2c:user-2"
	msg, _ := createAssistantMessages(t, ctx, store, otherScope)
	mapPlatformMessage(t, ctx, store, currentScope, "p-other-scope", msg)

	result, err := Apply(ctx, newRefTestOptions(store, newRefTestHistory(t)), share.ReferenceOptions{Platform: currentScope.Platform, ChatID: currentScope.ChatID, Owner: currentScope.Owner, ReplyID: "p-other-scope", Text: "继续", ChatType: currentScope.ChatType})
	if err != nil {
		t.Fatal(err)
	}
	want := "[引用#p-other-scope：bot:old]\n\n继续"
	if result.Text != want || result.ResumeSessionID != "" || result.ForkFromMessageID != "" {
		t.Fatalf("result = %#v, want fallback %q", result, want)
	}
}

func TestApplyForkCommandUsesReferencedAssistantID(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := referenceTestScope{Owner: contextinfo.ActorIdentity{Platform: "qqofficial", ID: "user-1"}, Platform: "qqofficial", ChatType: "private", ChatID: "user-1"}
	msg, _ := createAssistantMessages(t, ctx, store, scope)
	mapPlatformMessage(t, ctx, store, scope, "p-old", msg)

	deps := newRefTestOptions(store, newRefTestHistory(t))
	for _, tc := range []struct {
		text, prefix string
		prefixes     []string
	}{
		{text: "/fork", prefix: "/", prefixes: []string{"/"}},
		{text: "!fork", prefix: "!", prefixes: []string{"!"}},
		{text: "!!fork", prefix: "!!", prefixes: []string{"!", "!!"}},
		{text: "!!fork", prefix: "!!", prefixes: []string{"!!", "!"}},
		{text: "!!FORK", prefix: "!!", prefixes: []string{"!", "!!"}},
		{text: "！！fork", prefix: "！！", prefixes: []string{"！", "！！"}},
	} {
		t.Run(tc.text+tc.prefixes[0], func(t *testing.T) {
			deps.ParseCommand = func(text string) command.Parsed { return command.Parse(text, tc.prefixes) }
			result, err := Apply(ctx, deps, share.ReferenceOptions{Platform: scope.Platform, ChatID: scope.ChatID, Owner: scope.Owner, ReplyID: "p-old", Text: tc.text, ChatType: scope.ChatType})
			if err != nil {
				t.Fatal(err)
			}
			want := tc.prefix + "fork " + msg.ID
			if result.Text != want {
				t.Fatalf("text = %q, want %q", result.Text, want)
			}
		})
	}
}

func createAssistantMessages(t *testing.T, ctx context.Context, store storage.Store, scope referenceTestScope) (*storage.Message, *storage.Message) {
	t.Helper()
	s := &storage.Session{ID: storage.NewID(), Owner: scope.Owner, Platform: scope.Platform, ChatID: scope.ChatID, Mode: storage.SessionModeWork, Title: "source", ChatType: scope.ChatType}
	if err := store.Sessions().Create(ctx, s); err != nil {
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

func mapPlatformMessage(t *testing.T, ctx context.Context, store storage.Store, scope referenceTestScope, platformMessageID string, msg *storage.Message) {
	t.Helper()
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: scope.Platform, ChatID: scope.ChatID, PlatformMessageID: platformMessageID, MessageID: msg.ID, SessionID: msg.SessionID, ChatType: scope.ChatType}); err != nil {
		t.Fatalf("map message: %v", err)
	}
}

func newRefTestOptions(store storage.Store, history storage.ChatHistoryRepository) Options {
	service := control.NewService(control.Config{}, control.ServiceDeps{Sessions: store.Sessions(), Messages: store.Messages(), IsSuperadmin: func(scope sessionshare.Scope) bool { return scope.Owner.ID == "admin" }})
	return Options{
		ParseCommand:          func(text string) command.Parsed { return command.Parse(text, []string{"/"}) },
		FindByPlatformMessage: store.Messages().FindByPlatformMessage,
		ResolveContinuation: func(ctx context.Context, opts share.ReferenceOptions, message storage.Message) (string, string, bool, error) {
			return service.ResolveReference(ctx, sessionshare.Scope{Owner: opts.Owner, Platform: opts.Platform, ChatType: opts.ChatType, ChatID: opts.ChatID}, message)
		},
		FindHistory: store.Media().FindHistory,
		GetMedia:    store.Media().Get,
		FindOutputs: store.Media().FindOutputs,
		ChatHistory: history,
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

func newRefTestHistory(t *testing.T) storage.ChatHistoryRepository {
	t.Helper()
	history, err := sqlite.NewChatHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	return history.Repository()
}
