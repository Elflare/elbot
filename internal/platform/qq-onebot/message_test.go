package qqonebot

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/output"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestNewFromPlatformConfig(t *testing.T) {
	adapter, err := NewFromPlatformConfig(map[string]any{
		"enabled":          true,
		"ws_url":           "ws://example",
		"trigger_keywords": []any{"芙莉丝"},
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewFromPlatformConfig: %v", err)
	}
	if !adapter.Enabled() || adapter.cfg.URL != "ws://example" || len(adapter.cfg.TriggerKeywords) != 1 || adapter.cfg.TriggerKeywords[0] != "芙莉丝" {
		t.Fatalf("adapter config = %#v", adapter.cfg)
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
	if !strings.HasSuffix(pages[0], "……（1/3）") || !strings.HasSuffix(pages[1], "……（2/3）") || !strings.HasSuffix(pages[2], "……（3/3）") {
		t.Fatalf("pages = %#v", pages)
	}
	if got := len([]rune(strings.TrimSuffix(pages[0], "……（1/3）"))); got != qqTextPageRunes {
		t.Fatalf("first page body runes = %d", got)
	}
	if got := len([]rune(strings.TrimSuffix(pages[2], "……（3/3）"))); got != 1 {
		t.Fatalf("last page body runes = %d", got)
	}
}

func TestQQTextPagesSplitsChineseRunes(t *testing.T) {
	pages := qqTextPages(strings.Repeat("娅", qqTextPageRunes) + "芙")
	if len(pages) != 2 {
		t.Fatalf("page count = %d", len(pages))
	}
	if !strings.HasPrefix(pages[1], "芙……（2/2）") {
		t.Fatalf("second page = %q", pages[1])
	}
}

func TestNormalizeArrayMessage(t *testing.T) {
	msg := normalizeMessage([]byte(`[

		{"type":"at","data":{"qq":"1000"}},
		{"type":"text","data":{"text":"  hello  "}},
		{"type":"reply","data":{"id":"42"}},
		{"type":"image","data":{"file":"a.jpg"}}
	]`), "", 1000)
	if !msg.AtSelf || msg.ReplyID != "42" || msg.Text != "hello [图片]" {
		t.Fatalf("message = %#v", msg)
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

func TestImageFileDataURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.png")
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	url, err := imageFileDataURL(path)
	if err != nil {
		t.Fatalf("imageFileDataURL: %v", err)
	}
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("url = %q", url)
	}
}

func TestNormalizeArrayImageAndFileSegments(t *testing.T) {
	msg := normalizeMessage([]byte(`[
		{"type":"text","data":{"text":"看"}},
		{"type":"image","data":{"file":"a.jpg","url":"https://example.com/a.jpg"}},
		{"type":"record","data":{"file":"v.amr"}}
	]`), "", 1000)
	if msg.Text != "看[图片][语音]" {
		t.Fatalf("text = %q", msg.Text)
	}
	if len(msg.Segments) != 3 || msg.Segments[1].Type != "image" || msg.Segments[1].URL != "https://example.com/a.jpg" || msg.Segments[2].Type != "file" || msg.Segments[2].Text != "语音" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
}

func TestNormalizeStringifiedArrayImageIgnoresRawMessage(t *testing.T) {
	raw := []byte(`"[{\"type\":\"image\",\"data\":{\"file\":\"E50BAC9EAA237E638057A4C662990635.jpg\",\"subType\":1,\"url\":\"https://multimedia.nt.qq.com.cn/download?appid=1406&fileid=abc&spec=0&rkey=xyz\",\"file_size\":\"1349\"}}]"`)
	msg := normalizeMessage(raw, `raw fallback must not be used`, 1000)
	if msg.Text != "[图片]" {
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

func TestShouldHandleGroupMessage(t *testing.T) {
	adapter := New(Config{Enabled: true, URL: "ws://127.0.0.1:6700/", TriggerKeywords: []string{"芙莉丝"}}, nil, nil)

	event := Event{MessageType: "group"}
	if adapter.shouldHandle(event, NormalizedMessage{Text: "hello"}) {
		t.Fatal("unexpectedly handled plain group message")
	}
	if !adapter.shouldHandle(event, NormalizedMessage{AtSelf: true, Text: "hello"}) {
		t.Fatal("expected at-self group message to be handled")
	}
	if !adapter.shouldHandle(event, NormalizedMessage{Text: "/status"}) {
		t.Fatal("expected slash command to be handled")
	}
	if !adapter.shouldHandle(event, NormalizedMessage{Text: "芙莉丝你好"}) {
		t.Fatal("expected trigger keyword group message to be handled")
	}
	if adapter.shouldHandle(event, NormalizedMessage{Text: "你好芙莉丝"}) {
		t.Fatal("did not expect non-prefix trigger keyword to be handled")
	}
}

func TestHandleEventStripsTriggerKeywordOnly(t *testing.T) {
	adapter := New(Config{Enabled: true, URL: "ws://127.0.0.1:6700/", TriggerKeywords: []string{"芙莉丝"}}, nil, nil)
	handler := &captureHandler{}
	adapter.handleEvent(context.Background(), handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, RawMessage: "芙莉丝，你好"})
	if handler.text != "，你好" {
		t.Fatalf("handled text = %q", handler.text)
	}
}

type captureHandler struct {
	text string
}

func (h *captureHandler) HandleMessage(ctx context.Context, text string) error {
	h.text = text
	return nil
}

func TestCommandWithReferenceFork(t *testing.T) {
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
	assistant := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "77", MessageID: assistant.ID, SessionID: session.ID}); err != nil {
		t.Fatalf("map platform message: %v", err)
	}

	adapter := New(Config{Enabled: true, URL: "ws://127.0.0.1:6700/"}, store, nil)
	got := adapter.commandWithReference(Event{MessageType: "group", GroupID: 9}, "77", "/fork")
	if got != "/fork "+assistant.ID {
		t.Fatalf("command = %q", got)
	}
}

func TestForkableReferenceMessageIDRequiresOwnAssistantSession(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	own := &storage.Session{OwnerID: "qqonebot:1", Platform: "qqonebot", PlatformScopeID: "group:9", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "own"}
	other := &storage.Session{OwnerID: "qqonebot:2", Platform: "qqonebot", PlatformScopeID: "group:9", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "other"}
	for _, session := range []*storage.Session{own, other} {
		if err := store.Sessions().Create(ctx, session); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	base := storage.Now()
	firstAssistant := &storage.Message{SessionID: own.ID, Role: storage.RoleAssistant, Content: "first answer", CreatedAt: base}
	latestAssistant := &storage.Message{SessionID: own.ID, Role: storage.RoleAssistant, Content: "latest answer", CreatedAt: base.Add(3 * time.Second)}
	otherAssistant := &storage.Message{SessionID: other.ID, Role: storage.RoleAssistant, Content: "other answer", CreatedAt: base.Add(time.Second)}
	ownUser := &storage.Message{SessionID: own.ID, Role: storage.RoleUser, Content: "own user", CreatedAt: base.Add(2 * time.Second)}
	for _, msg := range []*storage.Message{firstAssistant, otherAssistant, ownUser, latestAssistant} {
		if err := store.Messages().Append(ctx, msg); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	maps := []storage.PlatformMessageMap{
		{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "first-assistant", MessageID: firstAssistant.ID, SessionID: own.ID},
		{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "latest-assistant", MessageID: latestAssistant.ID, SessionID: own.ID},
		{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "other-assistant", MessageID: otherAssistant.ID, SessionID: other.ID},
		{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "own-user", MessageID: ownUser.ID, SessionID: own.ID},
	}
	for _, mapping := range maps {
		if err := store.Messages().MapPlatformMessage(ctx, mapping); err != nil {
			t.Fatalf("map platform message: %v", err)
		}
	}

	adapter := New(Config{Enabled: true, URL: "ws://127.0.0.1:6700/"}, store, nil)
	event := Event{MessageType: "group", GroupID: 9, UserID: 1}
	if got := adapter.forkableReferenceMessageID(ctx, event, "first-assistant"); got != firstAssistant.ID {
		t.Fatalf("historical assistant fork id = %q, want %q", got, firstAssistant.ID)
	}
	if got := adapter.forkableReferenceMessageID(ctx, event, "latest-assistant"); got != "" {
		t.Fatalf("latest assistant should continue current conversation, got fork id %q", got)
	}
	if got := adapter.forkableReferenceMessageID(ctx, event, "other-assistant"); got != "" {
		t.Fatalf("other assistant should not fork, got %q", got)
	}
	if got := adapter.forkableReferenceMessageID(ctx, event, "own-user"); got != "" {
		t.Fatalf("user message should not fork, got %q", got)
	}
	handler := &captureHandler{}
	adapter.handleEvent(ctx, handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, Message: []byte(`[{"type":"reply","data":{"id":"latest-assistant"}},{"type":"text","data":{"text":"继续"}}]`)})
	if handler.text != "继续" {
		t.Fatalf("latest assistant reference text = %q, want direct continuation", handler.text)
	}
}

func TestFinalMessageSegmentsIncludesReferenceImage(t *testing.T) {
	current := []platform.MessageSegment{{Type: platform.SegmentText, Text: "看这个"}}
	referenced := []platform.MessageSegment{{Type: platform.SegmentText, Text: "[图片]"}, {Type: platform.SegmentImage, URL: "https://example.com/a.jpg", Name: "a.jpg"}}

	segments := finalMessageSegments("[引用：用户(qq:1)]：[图片]\n\n看这个", current, referenced)
	if len(segments) != 2 {
		t.Fatalf("segments = %#v", segments)
	}
	if segments[0].Type != platform.SegmentText || !strings.Contains(segments[0].Text, "[引用：用户(qq:1)]：[图片]") {
		t.Fatalf("text segment = %#v", segments[0])
	}
	if segments[1].Type != platform.SegmentImage || segments[1].URL != "https://example.com/a.jpg" || segments[1].Name != "a.jpg" {
		t.Fatalf("image segment = %#v", segments[1])
	}
}

func TestOutputSegments(t *testing.T) {
	image := []byte("fake image")
	path := filepath.Join(t.TempDir(), "huaji.png")
	if err := os.WriteFile(path, image, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	segments, err := outputSegments(output.EmoticonPath("滑稽", path))
	if err != nil {
		t.Fatalf("outputSegments image: %v", err)
	}
	if len(segments) != 1 || segments[0].Type != "image" {
		t.Fatalf("image segments = %#v", segments)
	}
	file := segmentDataString(segments[0].Data, "file")
	if !strings.HasPrefix(file, "base64://") {
		t.Fatalf("image file = %q", file)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(file, "base64://"))
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	if string(decoded) != string(image) {
		t.Fatalf("decoded image = %q", decoded)
	}

	segments, err = outputSegments(output.At("123456"))
	if err != nil {
		t.Fatalf("outputSegments at: %v", err)
	}
	if len(segments) != 1 || segments[0].Type != "at" || segments[0].Data["qq"] != "123456" {
		t.Fatalf("at segments = %#v", segments)
	}
}

func TestIsBotReplyFallsBackToGetMessage(t *testing.T) {
	transport := newTestTransport(t, func(req request) response {
		if req.Action != "get_msg" {
			t.Fatalf("action = %q", req.Action)
		}
		return response{Status: "ok", Data: []byte(`{"user_id":1000}`), Echo: req.Echo}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL}, nil, nil)
	adapter.transport = transport

	if !adapter.shouldHandle(Event{MessageType: "group", SelfID: 1000}, NormalizedMessage{ReplyID: "77", Text: "继续"}) {
		t.Fatal("expected reply to bot message to be handled")
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
	adapter := New(Config{Enabled: true, URL: transport.URL}, store, nil)
	adapter.transport = transport

	got, segments := adapter.withReference(ctx, Event{MessageType: "group", SelfID: 1000, GroupID: 9}, "77", "继续")
	if got != "[引用：用户(qq:2)]：stored answer\n\n继续" {
		t.Fatalf("reference text = %q", got)
	}
	if len(segments) != 1 || segments[0].Type != platform.SegmentImage || segments[0].URL != "https://example.com/a.jpg" {
		t.Fatalf("reference segments = %#v", segments)
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
	transport := &Transport{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Timeout: time.Second}
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
		transport.Close(websocket.StatusNormalClosure, "test done")
	})
	return transport
}

func TestWithReferenceUsesShortFormat(t *testing.T) {
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
	assistant := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "77", MessageID: assistant.ID, SessionID: session.ID}); err != nil {
		t.Fatalf("map platform message: %v", err)
	}

	adapter := New(Config{Enabled: true, URL: "ws://127.0.0.1:6700/"}, store, nil)
	got, _ := adapter.withReference(ctx, Event{MessageType: "group", GroupID: 9}, "77", "继续")
	if got != "[引用：bot]：answer\n\n继续" {
		t.Fatalf("reference text = %q", got)
	}
}
