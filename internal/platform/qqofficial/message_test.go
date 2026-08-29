package qqofficial

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestHandleGroupAtMessageBuildsGroupContext(t *testing.T) {
	raw := json.RawMessage(`{"id":"msg-1","group_openid":"group-1"}`)
	adapter := New(Config{AppID: "bot-app", TriggerKeywords: []string{"芙莉丝"}}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleGroupMessage(context.Background(), handler, payload{ID: "event-1", Type: eventGroupAtMessageCreate, Data: raw}, inboundMessage{
		ID:          "msg-1",
		GroupOpenID: "group-1",
		Author:      inboundAuthor{MemberOpenID: "member-1"},
		Content:     "@机器人 你好",
	})

	if handler.text != "你好" {
		t.Fatalf("text = %q, want stripped group-at text", handler.text)
	}
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ActorID != "qqofficial:member-1" || msgCtx.PlatformUserID != "member-1" {
		t.Fatalf("actor/user = %q/%q", msgCtx.ActorID, msgCtx.PlatformUserID)
	}
	if msgCtx.ScopeID != "group:group-1" || msgCtx.ConversationKind != platform.ConversationGroup {
		t.Fatalf("scope/conversation = %q/%q", msgCtx.ScopeID, msgCtx.ConversationKind)
	}
	if msgCtx.Bot.UserID != "bot-app" || len(msgCtx.Mentions) != 1 || msgCtx.Mentions[0].UserID != "bot-app" {
		t.Fatalf("bot/mentions = %#v/%#v", msgCtx.Bot, msgCtx.Mentions)
	}
	if len(msgCtx.TriggerKeywords) != 1 || msgCtx.TriggerKeywords[0] != "芙莉丝" {
		t.Fatalf("trigger keywords = %#v", msgCtx.TriggerKeywords)
	}
	if string(msgCtx.PlatformMessage) != string(raw) || msgCtx.RawText != "你好" {
		t.Fatalf("raw context = %q/%q", msgCtx.PlatformMessage, msgCtx.RawText)
	}
	target, ok := handler.ctx.Value(targetKey{}).(sendTarget)
	if !ok || target.Kind != targetGroup || target.OpenID != "group-1" || target.MsgID != "msg-1" {
		t.Fatalf("target = %#v", target)
	}
}

func TestHandleOrdinaryGroupMessageKeepsWakeupInputs(t *testing.T) {
	adapter := New(Config{AppID: "bot-app", TriggerKeywords: []string{"芙莉丝"}}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleGroupMessage(context.Background(), handler, payload{Type: eventGroupMessageCreate}, inboundMessage{
		ID:          "msg-1",
		GroupOpenID: "group-1",
		Author:      inboundAuthor{MemberOpenID: "member-1"},
		Content:     "芙莉丝，你好",
	})

	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if handler.text != "芙莉丝，你好" || len(msgCtx.Mentions) != 0 {
		t.Fatalf("text/mentions = %q/%#v", handler.text, msgCtx.Mentions)
	}
	if msgCtx.ReplyToMessageID != "" || len(msgCtx.TriggerKeywords) != 1 {
		t.Fatalf("reply/keywords = %q/%#v", msgCtx.ReplyToMessageID, msgCtx.TriggerKeywords)
	}
}

func TestHandleGroupMessageRecordsChatHistoryBeforeWakeup(t *testing.T) {
	ctx := context.Background()
	historyStore, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "chat-history.db"))
	if err != nil {
		t.Fatalf("new chat history: %v", err)
	}
	defer historyStore.Close()
	history := historyStore.Repository()
	adapter := New(Config{}, nil, history, nil)
	handler := &captureHandler{}
	adapter.handleGroupMessage(ctx, handler, payload{Type: eventGroupMessageCreate}, inboundMessage{
		ID:          "msg-1",
		GroupOpenID: "group-1",
		Author:      inboundAuthor{MemberOpenID: "member-1"},
		Content:     "普通消息",
		Timestamp:   "2026-08-29T12:34:56+08:00",
		MessageReference: &messageReference{
			MessageID: "reply-1",
		},
	})

	row, err := history.GetByPlatformMessage(ctx, platformName, "group:group-1", "msg-1")
	if err != nil {
		t.Fatalf("get chat history: %v", err)
	}
	if row.ScopeType != "group" || row.SenderID != "member-1" || row.Text != "普通消息" {
		t.Fatalf("chat history = %#v", row)
	}
	if row.Raw != "普通消息" || row.ReplyToPlatformMessageID != "reply-1" {
		t.Fatalf("chat history raw/reply = %q/%q", row.Raw, row.ReplyToPlatformMessageID)
	}
	wantTime := time.Date(2026, 8, 29, 12, 34, 56, 0, time.FixedZone("UTC+8", 8*60*60))
	if !row.CreatedAt.Equal(wantTime) {
		t.Fatalf("created at = %v, want %v", row.CreatedAt, wantTime)
	}
}

func TestHandleGroupMessageAppliesAssistantReference(t *testing.T) {
	ctx := context.Background()
	store := newQQOfficialTestStore(t)
	scope := session.Scope{ActorID: "qqofficial:member-1", Platform: platformName, PlatformScopeID: "group:group-1"}
	first, _ := createQQOfficialAssistantMessages(t, ctx, store, scope)
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: platformName, PlatformScopeID: scope.PlatformScopeID, PlatformMessageID: "assistant-1", MessageID: first.ID, SessionID: first.SessionID}); err != nil {
		t.Fatalf("map assistant: %v", err)
	}
	adapter := New(Config{}, store, nil, nil)
	handler := &captureHandler{}
	adapter.handleGroupMessage(ctx, handler, payload{Type: eventGroupMessageCreate}, inboundMessage{
		ID:          "msg-1",
		GroupOpenID: "group-1",
		Author:      inboundAuthor{MemberOpenID: "member-1"},
		Content:     "继续",
		MessageReference: &messageReference{
			MessageID: "assistant-1",
		},
	})

	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ReplyToMessageID != "assistant-1" || msgCtx.ForkFromMessageID != first.ID {
		t.Fatalf("reply/fork = %q/%q", msgCtx.ReplyToMessageID, msgCtx.ForkFromMessageID)
	}
}

type asyncCaptureHandler struct {
	result chan platform.MessageContext
}

func (h *asyncCaptureHandler) HandleMessage(ctx context.Context, _ string) error {
	msg, _ := platform.MessageContextFrom(ctx)
	h.result <- msg
	return nil
}

func TestHandleDispatchRoutesOrdinaryGroupMessage(t *testing.T) {
	adapter := New(Config{}, nil, nil, nil)
	handler := &asyncCaptureHandler{result: make(chan platform.MessageContext, 1)}
	data := json.RawMessage(`{"id":"msg-1","group_openid":"group-1","author":{"member_openid":"member-1"},"content":"hello"}`)
	if err := adapter.handleDispatch(context.Background(), handler, payload{Type: eventGroupMessageCreate, Data: data}, &gatewayState{}); err != nil {
		t.Fatalf("handle dispatch: %v", err)
	}
	select {
	case msg := <-handler.result:
		if msg.ScopeID != "group:group-1" {
			t.Fatalf("scope = %q", msg.ScopeID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for group dispatch")
	}
}

func TestHandleC2CMessageUsesCanonicalActorID(t *testing.T) {
	adapter := New(Config{}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
		ID:      "msg-1",
		Author:  inboundAuthor{UserOpenID: "user-1"},
		Content: "你好",
	})
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ActorID != "qqofficial:user-1" {
		t.Fatalf("actor id = %q, want qqofficial:user-1", msgCtx.ActorID)
	}
	if msgCtx.PlatformUserID != "user-1" {
		t.Fatalf("platform user id = %q, want user-1", msgCtx.PlatformUserID)
	}
}

func TestHandleC2CMessageAddsFallbackReferenceText(t *testing.T) {
	adapter := New(Config{}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
		ID:      "msg-1",
		Author:  inboundAuthor{UserOpenID: "user-1"},
		Content: "你看看有没",
		MessageReference: &messageReference{
			MessageID: "notice-1",
			Content:   "已保存附件：attachment-1\n路径：/tmp/attachment-1",
		},
	})

	want := "[引用]：已保存附件：attachment-1\n路径：/tmp/attachment-1\n\n你看看有没"
	if handler.text != "你看看有没" {
		t.Fatalf("text = %q, want current text", handler.text)
	}
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ContextText != want {
		t.Fatalf("context text = %q, want %q", msgCtx.ContextText, want)
	}
	if msgCtx.Reply.MessageID != "notice-1" || msgCtx.Reply.Text != "已保存附件：attachment-1\n路径：/tmp/attachment-1" {
		t.Fatalf("reply = %#v", msgCtx.Reply)
	}
}

func TestHandleC2CMessageForksOwnOlderAssistantReference(t *testing.T) {
	ctx := context.Background()
	store := newQQOfficialTestStore(t)
	adapter := New(Config{}, store, nil, nil)
	svc := session.NewService(store)
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: platformName, PlatformScopeID: "c2c:user-1"}
	s, err := svc.Create(ctx, scope, session.CreateRequest{Title: "source"})
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
	adapter.handleC2CMessage(ctx, handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
		ID:               "msg-1",
		Author:           inboundAuthor{UserOpenID: "user-1"},
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
	imageURL := "https://example.com/image.png"
	adapter := New(Config{}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
		ID:     "msg-1",
		Author: inboundAuthor{UserOpenID: "user-1"},
		Attachments: []messageAttachment{{
			URL:         imageURL,
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
	if image.URL != imageURL {
		t.Fatalf("image URL = %q, want %q", image.URL, imageURL)
	}
}

func TestHandleC2CMessageStickerAttachmentStripsFaceFallback(t *testing.T) {
	adapter := New(Config{}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
		ID:      "msg-1",
		Author:  inboundAuthor{UserOpenID: "user-1"},
		Content: `<faceType=6,faceId="0",ext="eyJ0ZXh0IjoiIn0=">`,
		Attachments: []messageAttachment{{
			URL:         "https://example.com/sticker.jpg",
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
	adapter := New(Config{}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
		ID:      "msg-1",
		Author:  inboundAuthor{UserOpenID: "user-1"},
		Content: `看看这个<faceType=6,faceId="0",ext="eyJ0ZXh0IjoiIn0=">`,
		Attachments: []messageAttachment{{
			URL:         "https://example.com/sticker.jpg",
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

	adapter := New(Config{AttachmentDir: t.TempDir()}, nil, nil, nil)
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

func TestPrepareSourceUsesStructuredSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	httpSource, err := prepareSource("https://example.com/a.png", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if httpSource.URL != "https://example.com/a.png" || len(httpSource.Data) != 0 {
		t.Fatalf("http source = %#v", httpSource)
	}

	base64Source, err := prepareSource("", "", []byte("from-base64"))
	if err != nil {
		t.Fatal(err)
	}
	if string(base64Source.Data) != "from-base64" {
		t.Fatalf("base64 data = %q", string(base64Source.Data))
	}

	fileSource, err := prepareSource("", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(fileSource.Data) != "from-file" {
		t.Fatalf("file data = %q", string(fileSource.Data))
	}
}

func TestHandleC2CMessageContinuesLatestAssistantReference(t *testing.T) {
	ctx := context.Background()
	store := newQQOfficialTestStore(t)
	adapter := New(Config{}, store, nil, nil)
	scope := session.Scope{ActorID: "qqofficial:user-1", Platform: platformName, PlatformScopeID: "c2c:user-1"}
	_, latest := createQQOfficialAssistantMessages(t, ctx, store, scope)
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: platformName, PlatformScopeID: scope.PlatformScopeID, PlatformMessageID: "platform-latest", MessageID: latest.ID, SessionID: latest.SessionID}); err != nil {
		t.Fatalf("map latest: %v", err)
	}

	handler := &captureHandler{}
	adapter.handleC2CMessage(ctx, handler, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
		ID:               "msg-1",
		Author:           inboundAuthor{UserOpenID: "user-1"},
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
	s, err := session.NewService(store).Create(ctx, scope, session.CreateRequest{Title: "source"})
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
