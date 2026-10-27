// message_test.go 验证入站身份、唤醒事实、引用结果、历史写入、附件转换与连接通知。

package qqofficial

import (
	"context"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type captureHandler struct {
	text string
	ctx  context.Context
}

func (h *captureHandler) HandleMessage(ctx context.Context, text string, msg share.MessageContext) error {
	ctx = share.WithMessageContext(ctx, msg)
	h.ctx = ctx
	h.text = text
	return nil
}

func TestHandleGroupAtMessageBuildsGroupContext(t *testing.T) {
	raw := json.RawMessage(`{"id":"msg-1","group_openid":"group-1"}`)
	adapter := newTestAdapter(t, Config{AppID: "bot-app", TriggerKeywords: []string{"芙莉丝"}}, nil)
	handler := &captureHandler{}
	adapter.handleGroupMessage(context.Background(), handler.HandleMessage, payload{ID: "event-1", Type: eventGroupAtMessageCreate, Data: raw}, inboundMessage{
		ID:          "msg-1",
		GroupOpenID: "group-1",
		Author:      inboundAuthor{MemberOpenID: "member-1"},
		Content:     "@机器人 你好",
	})

	if handler.text != "你好" {
		t.Fatalf("text = %q, want stripped group-at text", handler.text)
	}
	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.Conversation.Identity.ActorID != "qqofficial:member-1" || msgCtx.Conversation.Identity.PlatformUserID != "member-1" {
		t.Fatalf("actor/user = %q/%q", msgCtx.Conversation.Identity.ActorID, msgCtx.Conversation.Identity.PlatformUserID)
	}
	if msgCtx.Conversation.Source.ScopeID != "group:group-1" || msgCtx.Conversation.Source.ConversationKind != contextinfo.ConversationGroup || msgCtx.Conversation.Source.ConversationID != "group-1" {
		t.Fatalf("scope/conversation = %q/%q", msgCtx.Conversation.Source.ScopeID, msgCtx.Conversation.Source.ConversationKind)
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
	target, err := adapter.contextTarget(handler.ctx)
	if err != nil || target.Kind != targetGroup || target.OpenID != "group-1" || target.MsgID != "msg-1" {
		t.Fatalf("target = %#v", target)
	}
}

func TestHandleOrdinaryGroupMessageKeepsWakeupInputs(t *testing.T) {
	adapter := newTestAdapter(t, Config{AppID: "bot-app", TriggerKeywords: []string{"芙莉丝"}}, nil)
	handler := &captureHandler{}
	adapter.handleGroupMessage(context.Background(), handler.HandleMessage, payload{Type: eventGroupMessageCreate}, inboundMessage{
		ID:          "msg-1",
		GroupOpenID: "group-1",
		Author:      inboundAuthor{MemberOpenID: "member-1"},
		Content:     "芙莉丝，你好",
	})

	msgCtx, ok := share.MessageContextFrom(handler.ctx)
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
	adapter := newTestAdapter(t, Config{}, history)
	mapTestReference(t, adapter, "group:group-1", "REFIDX_reply-1", "reply-1")
	adapter.referenceResolver = func(_ context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
		if opts.ReplyID != "reply-1" {
			t.Fatalf("reply = %q", opts.ReplyID)
		}
		return share.ReferenceResult{Reply: share.ReplyContext{MessageID: opts.ReplyID}}, nil
	}
	handler := &captureHandler{}
	adapter.handleGroupMessage(ctx, handler.HandleMessage, payload{Type: eventGroupMessageCreate}, inboundMessage{
		ID:           "msg-1",
		GroupOpenID:  "group-1",
		Author:       inboundAuthor{MemberOpenID: "member-1"},
		Content:      "普通消息",
		Timestamp:    "2026-08-29T12:34:56+08:00",
		MessageScene: messageScene{Ext: []string{"ref_msg_idx=REFIDX_reply-1"}},
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
	a := newTestAdapter(t, Config{}, nil)
	mapTestReference(t, a, "group:group-1", "REFIDX_assistant-1", "assistant-1")
	called := false
	a.referenceResolver = func(_ context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
		called = true
		if opts.Platform != platformName || opts.ScopeID != "group:group-1" || opts.ReplyID != "assistant-1" || opts.Text != "继续" {
			t.Fatalf("options = %+v", opts)
		}
		return share.ReferenceResult{ForkFromMessageID: "fork-message", Reply: share.ReplyContext{MessageID: opts.ReplyID}, Text: "resolved"}, nil
	}
	h := &captureHandler{}
	a.handleGroupMessage(t.Context(), h.HandleMessage, payload{}, inboundMessage{ID: "msg", GroupOpenID: "group-1", Author: inboundAuthor{MemberOpenID: "member-1", UserOpenID: "user-1"}, Content: "继续", MessageScene: messageScene{Ext: []string{"ref_msg_idx=REFIDX_assistant-1"}}})
	msg, _ := share.MessageContextFrom(h.ctx)
	if !called || msg.ForkFromMessageID != "fork-message" || msg.ReplyToMessageID != "assistant-1" || msg.ContextText != "resolved" || h.text != "继续" {
		t.Fatalf("message = %+v", msg)
	}
}

type asyncCaptureHandler struct {
	result chan share.MessageContext
}

func (h *asyncCaptureHandler) HandleMessage(ctx context.Context, _ string, msg share.MessageContext) error {
	h.result <- msg
	return nil
}

func TestHandleDispatchRoutesOrdinaryGroupMessage(t *testing.T) {
	adapter := newTestAdapter(t, Config{}, nil)
	handler := &asyncCaptureHandler{result: make(chan share.MessageContext, 1)}
	data := json.RawMessage(`{"id":"msg-1","group_openid":"group-1","author":{"member_openid":"member-1"},"content":"hello"}`)
	if err := adapter.handleDispatch(context.Background(), handler.HandleMessage, payload{Type: eventGroupMessageCreate, Data: data}, &gatewayState{}); err != nil {
		t.Fatalf("handle dispatch: %v", err)
	}
	select {
	case msg := <-handler.result:
		if msg.Conversation.Source.ScopeID != "group:group-1" {
			t.Fatalf("scope = %q", msg.Conversation.Source.ScopeID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for group dispatch")
	}
}

func TestHandleC2CMessageUsesCanonicalActorID(t *testing.T) {
	adapter := newTestAdapter(t, Config{}, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler.HandleMessage, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
		ID:      "msg-1",
		Author:  inboundAuthor{UserOpenID: "user-1"},
		Content: "你好",
	})
	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.Conversation.Identity.ActorID != "qqofficial:user-1" {
		t.Fatalf("actor id = %q, want qqofficial:user-1", msgCtx.Conversation.Identity.ActorID)
	}
	if msgCtx.Conversation.Identity.PlatformUserID != "user-1" {
		t.Fatalf("platform user id = %q, want user-1", msgCtx.Conversation.Identity.PlatformUserID)
	}
}

func TestHandleC2CMessageAddsFallbackReferenceText(t *testing.T) {
	adapter := newTestAdapter(t, Config{}, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler.HandleMessage, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
		ID:          "msg-1",
		Author:      inboundAuthor{UserOpenID: "user-1"},
		Content:     "你看看有没",
		MessageType: 103,
		MsgElements: []messageElement{{MsgIdx: "REFIDX_notice-1", Content: "已保存附件：attachment-1\n路径：/tmp/attachment-1"}},
	})

	want := "[引用:已保存附件：attachment-1\n路径：/tmp/attachment-1]\n\n你看看有没"
	if handler.text != "你看看有没" {
		t.Fatalf("text = %q, want current text", handler.text)
	}
	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ContextText != want {
		t.Fatalf("context text = %q, want %q", msgCtx.ContextText, want)
	}
	if msgCtx.Reply.MessageID != "" || msgCtx.Reply.Text != "已保存附件：attachment-1\n路径：/tmp/attachment-1" {
		t.Fatalf("reply = %#v", msgCtx.Reply)
	}
}

func TestHandleC2CMessageForksOwnOlderAssistantReference(t *testing.T) {
	a := newTestAdapter(t, Config{}, nil)
	mapTestReference(t, a, "c2c:user-1", "REFIDX_old", "platform-old")
	called := false
	a.referenceResolver = func(_ context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
		called = true
		if opts.Platform != platformName || opts.ScopeID != "c2c:user-1" || opts.ReplyID != "platform-old" || opts.Text != "继续" {
			t.Fatalf("options = %+v", opts)
		}
		return share.ReferenceResult{ForkFromMessageID: "fork-message", Reply: share.ReplyContext{MessageID: opts.ReplyID}, Text: "resolved"}, nil
	}
	h := &captureHandler{}
	a.handleC2CMessage(t.Context(), h.HandleMessage, payload{}, inboundMessage{ID: "msg", GroupOpenID: "group-1", Author: inboundAuthor{MemberOpenID: "member-1", UserOpenID: "user-1"}, Content: "继续", MessageScene: messageScene{Ext: []string{"ref_msg_idx=REFIDX_old"}}})
	msg, _ := share.MessageContextFrom(h.ctx)
	if !called || msg.ForkFromMessageID != "fork-message" || msg.ReplyToMessageID != "platform-old" || msg.ContextText != "resolved" || h.text != "继续" {
		t.Fatalf("message = %+v", msg)
	}
}

func TestHandleC2CMessageImageAttachmentReachesHandler(t *testing.T) {
	imageURL := "https://example.com/image.png"
	adapter := newTestAdapter(t, Config{}, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler.HandleMessage, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
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
	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if len(msgCtx.Segments) != 1 {
		t.Fatalf("segments len = %d, want 1", len(msgCtx.Segments))
	}
	image := msgCtx.Segments[0]
	if image.Type != share.SegmentImage {
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
	adapter := newTestAdapter(t, Config{}, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler.HandleMessage, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
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
	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if len(msgCtx.Segments) != 1 || msgCtx.Segments[0].Type != share.SegmentImage {
		t.Fatalf("segments = %#v, want image only", msgCtx.Segments)
	}
}

func TestHandleC2CMessageTextAndStickerStripsOnlyFaceFallback(t *testing.T) {
	adapter := newTestAdapter(t, Config{}, nil)
	handler := &captureHandler{}
	adapter.handleC2CMessage(context.Background(), handler.HandleMessage, payload{ID: "event-1", Type: eventC2CMessageCreate}, inboundMessage{
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
	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if len(msgCtx.Segments) != 2 || msgCtx.Segments[0].Text != "看看这个" || msgCtx.Segments[1].Type != share.SegmentImage {
		t.Fatalf("segments = %#v, want text + image", msgCtx.Segments)
	}
}

func TestInboundAttachmentsKeepRawFile(t *testing.T) {
	segments := inboundAttachmentSegments([]messageAttachment{{URL: "https://example.com/file", ContentType: "file", Filename: "test.txt", Size: 9}})
	if len(segments) != 1 || segments[0].Type != share.SegmentFile || segments[0].URL != "https://example.com/file" || segments[0].Name != "test.txt" || segments[0].Size != 9 || segments[0].MIMEType != "" {
		t.Fatalf("raw segments = %#v", segments)
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

func TestHandleC2CMessageResumesLatestAssistantReference(t *testing.T) {
	a := newTestAdapter(t, Config{}, nil)
	mapTestReference(t, a, "c2c:user-1", "REFIDX_latest", "platform-latest")
	called := false
	a.referenceResolver = func(_ context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
		called = true
		if opts.Platform != platformName || opts.ScopeID != "c2c:user-1" || opts.ReplyID != "platform-latest" || opts.Text != "继续" {
			t.Fatalf("options = %+v", opts)
		}
		return share.ReferenceResult{ResumeSessionID: "resume-session", Reply: share.ReplyContext{MessageID: opts.ReplyID}, Text: "resolved"}, nil
	}
	h := &captureHandler{}
	a.handleC2CMessage(t.Context(), h.HandleMessage, payload{}, inboundMessage{ID: "msg", GroupOpenID: "group-1", Author: inboundAuthor{MemberOpenID: "member-1", UserOpenID: "user-1"}, Content: "继续", MessageScene: messageScene{Ext: []string{"ref_msg_idx=REFIDX_latest"}}})
	msg, _ := share.MessageContextFrom(h.ctx)
	if !called || msg.ResumeSessionID != "resume-session" || msg.ForkFromMessageID != "" || msg.ReplyToMessageID != "platform-latest" || msg.ContextText != "resolved" || h.text != "继续" {
		t.Fatalf("message = %+v", msg)
	}
}

func TestReadyPublishesGlobalConnectionWithoutAddingResumeEvent(t *testing.T) {
	calls := 0
	connection, err := globalevents.PlatformConnected.Observe(func(_ context.Context, e globalevents.PlatformConnectedEvent) error {
		calls++
		if e.Platform != "qqofficial" {
			t.Errorf("platform=%q", e.Platform)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	adapter := newTestAdapter(t, Config{}, nil)
	state := &gatewayState{}
	for _, kind := range []string{eventReady, eventResumed, eventReady} {
		if err := adapter.handleDispatch(context.Background(), nil, payload{Type: kind, Data: json.RawMessage(`{"session_id":"test","user":{"id":"bot","username":"test"}}`)}, state); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("connections=%d", calls)
	}
}

func newTestAdapter(t *testing.T, cfg Config, history storage.ChatHistoryRepository) *Adapter {
	t.Helper()
	if history == nil {
		db, err := sqlite.NewChatHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		history = db.Repository()
	}
	raw := map[string]any{"enabled": cfg.Enabled, "app_id": cfg.AppID, "client_secret": cfg.ClientSecret, "trigger_keywords": cfg.TriggerKeywords}
	if cfg.MarkdownByDefault != nil {
		raw["markdown_by_default"] = *cfg.MarkdownByDefault
	}
	a, err := NewFromPlatformConfig(raw, Deps{CommonConfig: share.CommonConfig{Superadmins: cfg.Superadmins, CommandPrefixes: cfg.CommandPrefixes}, ChatHistory: history})
	if err != nil {
		t.Fatal(err)
	}
	a.referenceResolver = func(context.Context, share.ReferenceOptions) (share.ReferenceResult, error) {
		t.Fatal("unexpected reference resolution")
		return share.ReferenceResult{}, nil
	}
	t.Cleanup(func() {
		if err := a.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return a
}
