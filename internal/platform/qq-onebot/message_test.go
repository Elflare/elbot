// message_test.go 验证配置、文本分页、消息归一化、引用上下文、历史快照与延迟媒体解析。

package qqonebot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestNewFromPlatformConfig(t *testing.T) {
	adapter, err := NewFromPlatformConfig(map[string]any{
		"enabled":          true,
		"ws_url":           "ws://example",
		"trigger_keywords": []any{"芙莉丝"},
	}, Deps{CommonConfig: share.CommonConfig{ConfigDir: t.TempDir()}})
	if err != nil {
		t.Fatalf("NewFromPlatformConfig: %v", err)
	}
	if !adapter.Enabled() || adapter.cfg.URL != "ws://example" || len(adapter.cfg.TriggerKeywords) != 1 || adapter.cfg.TriggerKeywords[0] != "芙莉丝" || adapter.cfg.SendFileMode != sendFileModeBase64 {
		t.Fatalf("adapter config = %#v", adapter.cfg)
	}
}

func TestNewFromPlatformConfigSendFileMode(t *testing.T) {
	adapter, err := NewFromPlatformConfig(map[string]any{
		"send_file_mode": "file_uri",
	}, Deps{CommonConfig: share.CommonConfig{ConfigDir: t.TempDir()}})
	if err != nil {
		t.Fatalf("NewFromPlatformConfig: %v", err)
	}
	if adapter.cfg.SendFileMode != sendFileModeFileURI {
		t.Fatalf("send file mode = %q", adapter.cfg.SendFileMode)
	}
}

func TestNewFromPlatformConfigRejectsInvalidSendFileMode(t *testing.T) {
	_, err := NewFromPlatformConfig(map[string]any{
		"send_file_mode": "auto",
	}, Deps{CommonConfig: share.CommonConfig{ConfigDir: t.TempDir()}})
	if err == nil || !strings.Contains(err.Error(), "send_file_mode") {
		t.Fatalf("err = %v", err)
	}
}

func TestQQTextPagesKeepsShortText(t *testing.T) {
	pages := qqTextPages("短消息")
	if len(pages) != 1 || pages[0] != "短消息" {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestQQTextPagesSplitsLongText(t *testing.T) {
	pages := qqTextPages(strings.Repeat("a", qqTextPageRunes*2+1))
	if len(pages) != 3 {
		t.Fatalf("page count = %d", len(pages))
	}
	if strings.Join(pages, "") != strings.Repeat("a", qqTextPageRunes*2+1) {
		t.Fatal("pages changed the original text")
	}
	if got := len([]rune(pages[0])); got != qqTextPageRunes {
		t.Fatalf("first page body runes = %d", got)
	}
	if got := len([]rune(pages[2])); got != 1 {
		t.Fatalf("last page body runes = %d", got)
	}
}

func TestQQTextPagesSplitsChineseRunes(t *testing.T) {
	pages := qqTextPages(strings.Repeat("娅", qqTextPageRunes) + "芙")
	if len(pages) != 2 {
		t.Fatalf("page count = %d", len(pages))
	}
	if pages[1] != "芙" {
		t.Fatalf("second page = %q", pages[1])
	}
}

func TestQQTextPagesExactBoundaries(t *testing.T) {
	body := strings.Repeat("娅", qqTextPageRunes)
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{name: "one full page", text: body, want: []string{body}},
		{name: "two full pages", text: body + body, want: []string{body, body}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pages := qqTextPages(tc.text)
			if len(pages) != len(tc.want) {
				t.Fatalf("page count = %d, want %d", len(pages), len(tc.want))
			}
			for i, want := range tc.want {
				if pages[i] != want {
					t.Fatalf("page %d = %q, want %q", i+1, pages[i], want)
				}
			}
		})
	}
}

func TestQQTextPagesPreservesLastPageText(t *testing.T) {
	for _, tail := range []string{"正文……", "内容。<", "🔥\n\n  "} {
		t.Run(tail, func(t *testing.T) {
			pages := qqTextPages(strings.Repeat("娅", qqTextPageRunes) + tail)
			if len(pages) != 2 {
				t.Fatalf("page count = %d, want 2", len(pages))
			}
			if want := tail; pages[1] != want {
				t.Fatalf("last page = %q, want %q", pages[1], want)
			}
		})
	}
}

func TestImageTextDoesNotRepeatPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "image only"},
		{name: "caption", text: "看这张"},
		{name: "literal placeholder", text: "[图片]是我写的"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := convertSegments([]Segment{
				{Type: "text", Data: map[string]any{"text": tc.text}},
				{Type: "image", Data: map[string]any{"file": "a.png", "url": "https://example.com/a.png"}},
			}, 1000, ordinaryInput)
			if msg.Text != tc.text {
				t.Fatalf("text = %q, want user text %q without generated image placeholder", msg.Text, tc.text)
			}
			segments := finalMessageSegments(msg.Text, msg.Segments, nil)
			if len(segments) == 0 || segments[len(segments)-1].Type != share.SegmentImage {
				t.Fatalf("image lost: %#v", segments)
			}
			for _, segment := range segments {
				if segment.Type == share.SegmentText && segment.Text != tc.text {
					t.Fatalf("unexpected text: %q", segment.Text)
				}
			}
		})
	}
}

func TestNormalizeArrayMessage(t *testing.T) {
	msg := normalizeMessage([]byte(`[

		{"type":"at","data":{"qq":"1000"}},
		{"type":"text","data":{"text":"  hello  "}},
		{"type":"reply","data":{"id":"42"}},
		{"type":"image","data":{"file":"a.jpg"}}
	]`), "", 1000)
	if msg.ReplyID != "42" || msg.Text != "hello" {
		t.Fatalf("message = %#v", msg)
	}
	if len(msg.Mentions) != 1 || msg.Mentions[0].UserID != "1000" {
		t.Fatalf("mentions = %#v", msg.Mentions)
	}
	if len(msg.Segments) != 2 || msg.Segments[0].Type != "text" || msg.Segments[1].Type != "image" || msg.Segments[1].Name != "a.jpg" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
}

func TestNormalizeImageFileURL(t *testing.T) {
	msg := normalizeMessage([]byte(`[{"type":"image","data":{"file":"https://example.com/a.jpg"}}]`), "", 1000)
	if len(msg.Segments) != 1 || msg.Segments[0].URL != "https://example.com/a.jpg" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
}

func TestNormalizeArrayImageAndFileSegments(t *testing.T) {
	msg := normalizeMessage([]byte(`[
		{"type":"text","data":{"text":"看"}},
		{"type":"image","data":{"file":"a.jpg","url":"https://example.com/a.jpg"}},
		{"type":"record","data":{"file":"v.amr"}}
	]`), "", 1000)
	if msg.Text != "看[语音]" {
		t.Fatalf("text = %q", msg.Text)
	}
	if len(msg.Segments) != 3 || msg.Segments[1].Type != "image" || msg.Segments[1].URL != "https://example.com/a.jpg" || msg.Segments[2].Type != "file" || msg.Segments[2].Text != "语音" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
}

func TestNormalizeStringifiedArrayImageIgnoresRawMessage(t *testing.T) {
	raw := []byte(`"[{\"type\":\"image\",\"data\":{\"file\":\"E50BAC9EAA237E638057A4C662990635.jpg\",\"subType\":1,\"url\":\"https://multimedia.nt.qq.com.cn/download?appid=1406&fileid=abc&spec=0&rkey=xyz\",\"file_size\":\"1349\"}}]"`)
	msg := normalizeMessage(raw, `raw fallback must not be used`, 1000)
	if msg.Text != "" {
		t.Fatalf("text = %q", msg.Text)
	}
	if len(msg.Segments) != 1 || msg.Segments[0].Type != "image" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
	if msg.Segments[0].URL != "https://multimedia.nt.qq.com.cn/download?appid=1406&fileid=abc&spec=0&rkey=xyz" {
		t.Fatalf("url = %q", msg.Segments[0].URL)
	}
	if msg.Segments[0].Name != "E50BAC9EAA237E638057A4C662990635.jpg" {
		t.Fatalf("name = %q", msg.Segments[0].Name)
	}
}

func TestNormalizePlainTextDoesNotParseMarkup(t *testing.T) {
	msg := normalizeMessage(nil, `[image file=a.jpg url=https://example.com/a.jpg]`, 1000)
	if msg.Text != `[image file=a.jpg url=https://example.com/a.jpg]` {
		t.Fatalf("text = %q", msg.Text)
	}
	if len(msg.Segments) != 1 || msg.Segments[0].Type != "text" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
}

func TestHandleEventMediaRemainsRaw(t *testing.T) {
	for _, messageType := range []string{"private", "group"} {
		for _, raw := range []string{
			`[{"type":"file","data":{"file":"test.txt","url":"https://example.com/file","file_size":"9"}}]`,
			`[{"type":"image","data":{"file":"image-id"}}]`,
			`[{"type":"file","data":{"file":"test.txt","file_id":"id-1"}}]`,
		} {
			adapter := newTestAdapter(t, Config{Enabled: true, Superadmins: []string{"1"}}, nil, nil)
			adapter.transport = newTestTransport(t, func(req request) response {
				t.Errorf("unexpected eager API call: %s", req.Action)
				return response{Status: "failed", Echo: req.Echo}
			})
			handler := &captureHandler{}
			adapter.handleEvent(context.Background(), handler.HandleMessage, Event{MessageType: messageType, SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 7, Message: []byte(raw)})
			msg, ok := share.MessageContextFrom(handler.ctx)
			if handler.count != 1 || !ok || msg.MediaResolver != adapter || len(msg.Segments) < 1 {
				t.Fatalf("handler/raw context = %d/%#v", handler.count, msg)
			}
			segment := msg.Segments[len(msg.Segments)-1]
			if segment.MediaID != "" || segment.PlatformFileID == "" {
				t.Fatalf("raw media = %#v", segment)
			}
		}
	}
}

func TestResolveMediaUsesPlatformOnlyOnDemand(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      share.MessageSegmentType
		data      string
		url, path string
		limit     int64
		wantErr   bool
	}{
		{name: "file URL preferred", kind: share.SegmentFile, data: `{"file":"C:/remote.txt","url":"https://example.com/file","file_size":"9"}`, url: "https://example.com/file", limit: 10},
		{name: "file path", kind: share.SegmentFile, data: `{"file":"C:/remote.txt","file_size":"9"}`, path: "C:/remote.txt", limit: 10},
		{name: "file oversized", kind: share.SegmentFile, data: `{"file":"C:/remote.txt","file_size":"11"}`, limit: 10, wantErr: true},
		{name: "image URL preferred", kind: share.SegmentImage, data: `{"file":"C:/remote.png","url":"https://example.com/image"}`, url: "https://example.com/image", limit: 10},
		{name: "image path", kind: share.SegmentImage, data: `{"file":"C:/remote.png"}`, path: "C:/remote.png", limit: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			adapter := newTestAdapter(t, Config{}, nil, nil)
			adapter.transport = newTestTransport(t, func(req request) response {
				calls++
				action := "get_file"
				if tc.kind == share.SegmentImage {
					action = "get_image"
				}
				if req.Action != action || req.Params["file"] != "id-1" {
					t.Errorf("API request = %#v", req)
				}
				return response{Status: "ok", Data: []byte(tc.data), Echo: req.Echo}
			})
			got, err := adapter.ResolveMedia(context.Background(), share.MessageSegment{Type: share.MessageSegmentType(tc.kind), PlatformFileID: "id-1", Name: "display-name"}, tc.limit)
			if (err != nil) != tc.wantErr || calls != 1 || got.URL != tc.url || got.Path != tc.path {
				t.Fatalf("source/calls/error = %#v/%d/%v", got, calls, err)
			}
		})
	}
}

func TestHandleEventDeliversPlainGroupMessage(t *testing.T) {
	adapter := newTestAdapter(t, Config{Enabled: true, URL: "ws://127.0.0.1:6700/", TriggerKeywords: []string{"芙莉丝"}}, nil, nil)
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler.HandleMessage, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, RawMessage: "hello"})

	if handler.count != 1 || handler.text != "hello" {
		t.Fatalf("handler count/text = %d/%q", handler.count, handler.text)
	}
	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.Conversation.Source.ConversationKind != contextinfo.ConversationGroup || msgCtx.Conversation.Source.ConversationID != "9" || msgCtx.RawText != "hello" {
		t.Fatalf("message context = %#v", msgCtx)
	}
}

func TestHandleEventKeepsPlatformMessageForHooks(t *testing.T) {
	adapter := newTestAdapter(t, Config{Enabled: true, URL: "ws://127.0.0.1:6700/"}, nil, nil)
	handler := &captureHandler{}
	raw := []byte(`[{"type":"json","data":{"data":"{\"app\":\"miniapp\"}"}}]`)

	adapter.handleEvent(context.Background(), handler.HandleMessage, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, Message: raw})

	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if got := string(msgCtx.PlatformMessage); got != string(raw) {
		t.Fatalf("platform message = %q, want %q", got, raw)
	}
}

func TestHandleEventKeepsTriggerKeywordForUpperLayers(t *testing.T) {
	adapter := newTestAdapter(t, Config{Enabled: true, URL: "ws://127.0.0.1:6700/", TriggerKeywords: []string{"芙莉丝"}}, nil, nil)
	handler := &captureHandler{}
	adapter.handleEvent(context.Background(), handler.HandleMessage, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, RawMessage: "芙莉丝，你好"})
	if handler.text != "芙莉丝，你好" {
		t.Fatalf("handled text = %q", handler.text)
	}
}

func TestHandleEventAtUsesGroupMemberCard(t *testing.T) {
	transport := newTestTransport(t, func(req request) response {
		if req.Action != "get_group_member_info" {
			t.Fatalf("action = %q", req.Action)
		}
		return response{Status: "ok", Data: []byte(`{"user_id":2,"card":"群昵称","nickname":"普通昵称"}`), Echo: req.Echo}
	})
	adapter := newTestAdapter(t, Config{Enabled: true, URL: transport.URL}, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler.HandleMessage, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 7, Message: []byte(`[{"type":"text","data":{"text":"/status "}},{"type":"at","data":{"qq":"2"}}]`)})

	if handler.text != "/status [at 群昵称 qq:2]" {
		t.Fatalf("handler text = %q", handler.text)
	}
	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if len(msgCtx.Segments) != 2 || msgCtx.Segments[1].Text != "[at 群昵称 qq:2]" {
		t.Fatalf("segments = %#v", msgCtx.Segments)
	}
}

func TestHandleEventAtFallsBackToNickname(t *testing.T) {
	transport := newTestTransport(t, func(req request) response {
		if req.Action != "get_group_member_info" {
			t.Fatalf("action = %q", req.Action)
		}
		return response{Status: "ok", Data: []byte(`{"user_id":2,"nickname":"普通昵称"}`), Echo: req.Echo}
	})
	adapter := newTestAdapter(t, Config{Enabled: true, URL: transport.URL}, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler.HandleMessage, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 7, Message: []byte(`[{"type":"text","data":{"text":"/status "}},{"type":"at","data":{"qq":"2"}}]`)})

	if handler.text != "/status [at 普通昵称 qq:2]" {
		t.Fatalf("handler text = %q", handler.text)
	}
}

type captureHandler struct {
	ctx   context.Context
	text  string
	count int
}

func (h *captureHandler) HandleMessage(ctx context.Context, text string, message share.MessageContext) error {
	h.ctx = share.WithMessageContext(ctx, message)
	h.text = text
	h.count++
	return nil
}

func TestHandleEventPreservesReferenceDecision(t *testing.T) {
	for _, decision := range []share.ReferenceResult{
		{ForkFromMessageID: "earlier-answer"},
		{ResumeSessionID: "own-session"},
		{Text: "foreign answer context", Reply: share.ReplyContext{MessageID: "42", Text: "foreign answer"}},
	} {
		adapter := newTestAdapter(t, Config{Superadmins: []string{"1"}}, nil, nil)
		adapter.findByPlatformMessage = func(context.Context, string, string, string) (*storage.Message, error) {
			return &storage.Message{Role: storage.RoleAssistant}, nil
		}
		adapter.referenceResolver = func(_ context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
			if opts.Platform != "qqonebot" || opts.ScopeID != "group:9" || opts.ActorID != "qqonebot:1" || !opts.IsSuperadmin || opts.ReplyID != "42" || opts.Text != "继续" || opts.Fetch == nil || opts.Enrich == nil {
				t.Fatalf("reference options = %#v", opts)
			}
			return decision, nil
		}
		handler := &captureHandler{}
		adapter.handleEvent(t.Context(), handler.HandleMessage, Event{
			MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 7,
			Message: []byte(`[{"type":"reply","data":{"id":"42"}},{"type":"text","data":{"text":"继续"}}]`),
		})
		message, ok := share.MessageContextFrom(handler.ctx)
		if !ok || handler.text != "继续" || message.ForkFromMessageID != decision.ForkFromMessageID || message.ResumeSessionID != decision.ResumeSessionID || message.ContextText != decision.Text || message.Reply.MessageID != decision.Reply.MessageID {
			t.Fatalf("message = %#v, text = %q, decision = %#v", message, handler.text, decision)
		}
	}
}

func TestFinalMessageSegmentsIncludesReferenceImage(t *testing.T) {
	current := []share.MessageSegment{{Type: share.SegmentText, Text: "看这个"}}
	referenced := []share.MessageSegment{{Type: share.SegmentText, Text: "[图片]"}, {Type: share.SegmentImage, URL: "https://example.com/a.jpg", Name: "a.jpg"}}

	segments := finalMessageSegments("[引用：用户]：[图片]\n\n看这个", current, referenced)
	if len(segments) != 2 {
		t.Fatalf("segments = %#v", segments)
	}
	if segments[0].Type != share.SegmentText || !strings.Contains(segments[0].Text, "[引用：用户]：[图片]") {
		t.Fatalf("text segment = %#v", segments[0])
	}
	if segments[1].Type != share.SegmentImage || segments[1].URL != "https://example.com/a.jpg" || segments[1].Name != "a.jpg" {
		t.Fatalf("image segment = %#v", segments[1])
	}
}

func TestOutputSegments(t *testing.T) {
	segments, err := outputSegments(sendFileModeBase64, delivery.Emoticon("178", "滑稽", ""))
	if err != nil {
		t.Fatalf("outputSegments emoticon: %v", err)
	}
	if len(segments) != 1 || segments[0].Type != "face" || segmentDataString(segments[0].Data, "id") != "178" {
		t.Fatalf("emoticon segments = %#v", segments)
	}

	segments, err = outputSegments(sendFileModeBase64, delivery.At("123456"))
	if err != nil {
		t.Fatalf("outputSegments at: %v", err)
	}
	if len(segments) != 1 || segments[0].Type != "at" || segments[0].Data["qq"] != "123456" {
		t.Fatalf("at segments = %#v", segments)
	}
}

func TestHandleEventStoresReferenceSnapshot(t *testing.T) {
	ctx := context.Background()
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	repo := history.Repository()
	if err := repo.Append(ctx, &storage.ChatMessage{
		Platform: "qqonebot", PlatformScopeID: "group:9", ScopeType: "group", PlatformMessageID: "42",
		SenderID: "1001", SenderName: "被引用者", Text: "被引用内容",
	}); err != nil {
		t.Fatal(err)
	}
	adapter := newTestAdapter(t, Config{}, nil, repo)
	adapter.referenceResolver = func(_ context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
		if opts.ReplyID != "42" {
			t.Fatalf("reply ID = %q", opts.ReplyID)
		}
		reply := share.ReplyContext{MessageID: "42", SenderID: "1001", SenderName: "被引用者", Text: "被引用内容"}
		return share.ReferenceResult{Text: share.FormatReferenceText("qqonebot", reply, opts.Text), Reply: reply}, nil
	}
	handler := &captureHandler{}
	adapter.handleEvent(ctx, handler.HandleMessage, Event{
		MessageType: "group", SelfID: 9999, UserID: 2002, GroupID: 9, MessageID: 84,
		Sender:  Sender{UserID: 2002, Nickname: "回复者"},
		Message: []byte(`[{"type":"reply","data":{"id":"42"}},{"type":"text","data":{"text":"本次发送的内容"}}]`),
	})
	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	wantText := `[引用#42：被引用者(qq:1001):被引用内容]

本次发送的内容`
	if msgCtx.ContextText != wantText {
		t.Fatalf("context text = %q, want %q", msgCtx.ContextText, wantText)
	}
	row, err := repo.GetByPlatformMessage(ctx, "qqonebot", "group:9", "84")
	if err != nil {
		t.Fatal(err)
	}
	reply := storage.ChatMessageReply(*row)
	if reply.MessageID != "42" || reply.SenderID != "1001" || reply.SenderName != "被引用者" || reply.Text != "被引用内容" {
		t.Fatalf("stored reply = %#v", reply)
	}
}

func TestHandleEventFillsReplyToSenderID(t *testing.T) {
	transport := newTestTransport(t, func(req request) response {
		switch req.Action {
		case "get_msg":
			return response{Status: "ok", Data: []byte(`{"user_id":1000,"message":[]}`), Echo: req.Echo}
		default:
			t.Fatalf("action = %q", req.Action)
		}
		return response{}
	})
	adapter := newTestAdapter(t, Config{Enabled: true, URL: transport.URL}, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler.HandleMessage, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 7, Message: []byte(`[{"type":"reply","data":{"id":"77"}},{"type":"text","data":{"text":"继续"}}]`)})

	msgCtx, ok := share.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ReplyToSenderID != "1000" {
		t.Fatalf("reply sender = %q", msgCtx.ReplyToSenderID)
	}
}

func TestWithReferenceUsesGetMessageImageWhenStoreHasText(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	session := &storage.Session{OwnerID: "qqonebot:1", Platform: "qqonebot", PlatformScopeID: "group:9", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "s"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	assistant := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "stored answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "77", MessageID: assistant.ID, SessionID: session.ID}); err != nil {
		t.Fatalf("map platform message: %v", err)
	}

	transport := newTestTransport(t, func(req request) response {
		return response{Status: "ok", Data: []byte(`{"user_id":2,"sender":{"nickname":"用户"},"message":[{"type":"image","data":{"file":"a.jpg","url":"https://example.com/a.jpg"}}]}`), Echo: req.Echo}
	})
	adapter := newTestAdapter(t, Config{Enabled: true, URL: transport.URL}, store, nil)
	adapter.transport = transport

	ref, ok, err := adapter.referenceFetcher(Event{MessageType: "group", SelfID: 1000, GroupID: 9})(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("missing reference")
	}
	if ref.Label != "引用：用户" {
		t.Fatalf("reference label = %q", ref.Label)
	}
	if ref.SenderID != "2" || ref.SenderName != "用户" {
		t.Fatalf("reference sender = %#v", ref)
	}
	if len(ref.Segments) != 1 || ref.Segments[0].Type != share.SegmentImage || ref.Segments[0].URL != "https://example.com/a.jpg" {
		t.Fatalf("reference segments = %#v", ref.Segments)
	}
}

func newTestTransport(t *testing.T, handle func(request) response) *Transport {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		for {
			var req request
			if err := wsjson.Read(r.Context(), conn, &req); err != nil {
				return
			}
			resp := handle(req)
			if resp.Status == "" {
				resp.Status = "ok"
			}
			if resp.Retcode == 0 {
				resp.Retcode = 0
			}
			if resp.Echo == "" {
				resp.Echo = req.Echo
			}
			if err := wsjson.Write(r.Context(), conn, resp); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	transport := newTransport("ws"+strings.TrimPrefix(server.URL, "http"), "", time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("connect transport: %v", err)
	}
	go func() {
		for {
			if _, err := transport.Read(ctx); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		transport.Close(context.Background())
	})
	return transport
}

func newTestAdapter(t *testing.T, cfg Config, store storage.Store, history storage.ChatHistoryRepository) *Adapter {
	t.Helper()
	if store == nil {
		db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "store.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store = db
	}
	if history == nil {
		db, err := sqlite.NewChatHistory(context.Background(), filepath.Join(t.TempDir(), "history.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		history = db.Repository()
	}
	adapter, err := NewFromPlatformConfig(map[string]any{
		"enabled": cfg.Enabled, "ws_url": cfg.URL, "access_token": cfg.AccessToken, "access_token_env": cfg.AccessTokenEnv,
		"reconnect_interval_seconds": cfg.ReconnectIntervalSeconds, "api_timeout_seconds": cfg.APITimeoutSeconds,
		"trigger_keywords": cfg.TriggerKeywords, "send_file_mode": cfg.SendFileMode,
	}, Deps{
		CommonConfig: share.CommonConfig{
			Superadmins: cfg.Superadmins, CommandPrefixes: cfg.CommandPrefixes,
		},
		ChatHistory: history, FindByPlatformMessage: store.Messages().FindByPlatformMessage,
		FindOutputs: store.Media().FindOutputs,
		ReferenceResolver: func(_ context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
			return share.ReferenceResult{Text: opts.Text, Reply: share.ReplyContext{MessageID: opts.ReplyID}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.Background()) })
	return adapter
}
