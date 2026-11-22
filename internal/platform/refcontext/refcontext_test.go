package refcontext

import (
	"context"
	"elbot/internal/contextinfo"
	"fmt"
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

func TestReferenceHistoryNumberAndAssistant(t *testing.T) {
	for _, tc := range []struct {
		platform, sender, name, username, want string
		stored                                 bool
	}{
		{"qqofficial", "self", "昨日昵称", "Atri", "[引用 h1：昨日昵称（助手）：正文]", true},
		{"qqofficial", "self", "", "", "[引用 h1：助手：正文]", true},
		{"qqofficial", "other", "bot", "Atri", "[引用 h1：bot(id:other)：正文]", true},
		{"qqonebot", "self", "亚托莉", "", "[引用：亚托莉（助手）：正文]", false},
		{"telegram", "other", "Atri", "Atri", "[引用 h1：Atri(id:other)：正文]", true},
	} {
		ctx := t.Context()
		history := newRefTestHistory(t)
		chat := "target"
		if !tc.stored {
			chat = "other-chat"
		}
		row := &storage.ChatMessage{Platform: tc.platform, ChatType: "group", ChatID: chat, PlatformMessageID: "987", SenderID: tc.sender, SenderName: tc.name, Text: "正文"}
		if err := history.Append(ctx, row); err != nil {
			t.Fatal(err)
		}
		opts := share.ReferenceOptions{Bot: share.Identity{UserID: "self", Username: tc.username}, Platform: tc.platform, ChatType: "group", ChatID: "target", ReplyID: "987", Fetch: func(context.Context, string) (share.ReferencedMessage, bool, error) {
			return share.ReferencedMessage{SenderID: tc.sender, SenderName: tc.name, Text: "正文"}, true, nil
		}}
		deps := newRefTestOptions(newRefTestStore(t), history)
		deps.FindHistory = func(context.Context, string, contextinfo.ChatType, string, string) ([]storage.HistoryMedia, error) {
			t.Fatal("text-only reference queried media associations")
			return nil, nil
		}
		got, err := Apply(ctx, deps, opts)
		if err != nil || got.Text != tc.want || got.Reply.MessageID != opts.ReplyID {
			t.Fatalf("%s: got %q (%+v), err %v; want %q", tc.platform, got.Text, got.Reply, err, tc.want)
		}
		if tc.stored {
			target, err := history.GetBySeq(ctx, opts.Platform, opts.ChatType, opts.ChatID, got.Reply.HistorySeq, false)
			if err != nil || target.PlatformMessageID != opts.ReplyID {
				t.Fatalf("short ID lookup: %+v, %v", target, err)
			}
		}
	}
}
func TestUnrecordedReferenceUsesFetchedContent(t *testing.T) {
	deps := newRefTestOptions(newRefTestStore(t), newRefTestHistory(t))
	calls := 0
	for _, input := range []string{"说：收到", "/help"} {
		got, err := Apply(t.Context(), deps, share.ReferenceOptions{Platform: "qqonebot", ReplyID: "notice", Text: input, Fetch: func(context.Context, string) (share.ReferencedMessage, bool, error) {
			calls++
			return share.ReferencedMessage{Text: "可读正文但无短号"}, true, nil
		}})
		want := "[引用 未知用户：可读正文但无短号]"
		live := want + "\n" + input
		if input == "/help" {
			live = input
		}
		if err != nil || got.Text != live || got.Reply.Display != want || got.ForkFromMessageID != "" {
			t.Fatalf("%s: %+v, %v", input, got, err)
		}
	}
	if calls != 2 {
		t.Fatalf("fetches=%d", calls)
	}
}

func TestExplicitAssistantPagesUseOnlyDeliveredChatContent(t *testing.T) {
	ctx := t.Context()
	store, history := newRefTestStore(t), newRefTestHistory(t)
	scope := referenceTestScope{Owner: contextinfo.ActorIdentity{Platform: "telegram", ID: "owner"}, Platform: "telegram", ChatType: "group", ChatID: "room"}
	original, _ := createAssistantMessages(t, ctx, store, scope)
	for _, p := range []struct {
		id, text, chat string
		assistant      bool
	}{{"p1", "公开上页", "room", true}, {"unrelated", "群友插话", "room", false}, {"p2", "公开下页", "room", true}, {"other", "另一个群", "elsewhere", true}} {
		target := scope
		target.ChatID = p.chat
		if p.assistant {
			mapPlatformMessage(t, ctx, store, target, p.id, original)
		}
		if err := history.Append(ctx, &storage.ChatMessage{Platform: scope.Platform, ChatType: scope.ChatType, ChatID: p.chat, PlatformMessageID: p.id, Text: p.text, IsAssistant: p.assistant}); err != nil {
			t.Fatal(err)
		}
	}
	deps := newRefTestOptions(store, history)
	for _, id := range []string{"p1", "p2"} {
		got, err := Apply(ctx, deps, share.ReferenceOptions{Platform: scope.Platform, ChatType: scope.ChatType, ChatID: scope.ChatID, Owner: contextinfo.ActorIdentity{Platform: scope.Platform, ID: "someone-else"}, ReplyID: id, Text: "说明"})
		seq := 1
		if id == "p2" {
			seq = 3
		}
		want := fmt.Sprintf("[引用 h%d：助手：公开上页\n公开下页]", seq)
		if err != nil || got.Reply.Display != want || got.Text != want+"\n说明" || share.SegmentsText(got.DisplaySegments) != got.Text {
			t.Fatalf("%s: %+v, %v", id, got, err)
		}
	}
	rows, err := history.Search(ctx, storage.ChatHistorySearchRequest{Platform: scope.Platform, ChatType: scope.ChatType, ChatID: scope.ChatID, IncludeAssistant: true, QueryTerms: []string{"公开"}})
	if err != nil || len(rows) != 2 || rows[0].Text != "公开上页" || rows[1].Text != "公开下页" {
		t.Fatalf("search=%+v, %v", rows, err)
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
	if result.Text != "继续" || result.Reply.MessageID != "p-latest" || !result.Reply.IsAssistant || result.Reply.Text != "" || len(result.Reply.Segments) != 0 {
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
	want := "继续"
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
	want := "继续"
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
	want := "继续"
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
	if err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: msg.SessionID, PlatformMessageMaps: []storage.PlatformMessageMap{{Platform: scope.Platform, ChatID: scope.ChatID, PlatformMessageID: platformMessageID, MessageID: msg.ID, SessionID: msg.SessionID, ChatType: scope.ChatType}}}); err != nil {
		t.Fatalf("map message: %v", err)
	}
}

func newRefTestOptions(store storage.Store, history storage.ChatHistoryRepository) Options {
	service := control.NewService(control.Config{}, control.ServiceDeps{Sessions: store.Sessions(), Messages: store.Messages(), IsSuperadmin: func(scope sessionshare.Scope) bool { return scope.Owner.ID == "admin" }})
	return Options{
		ParseCommand:          func(text string) command.Parsed { return command.Parse(text, []string{"/"}) },
		FindByPlatformMessage: store.Messages().FindByPlatformMessage, PlatformMessageIDs: store.Messages().PlatformMessageIDs,
		ResolveContinuation: func(ctx context.Context, opts share.ReferenceOptions, message storage.Message) (string, string, error) {
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
